package flashfile

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	defaultMaxBytes      = 100 << 20
	defaultTimeout       = 2 * time.Minute
	defaultStaleAfter    = 24 * time.Hour
	defaultCleanupGrace  = 5 * time.Minute
	defaultMaxRedirects  = 3
	defaultMaxTotalBytes = 512 << 20
	defaultMaxFiles      = 128
	defaultMaxConcurrent = 2
)

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("ff00::/8"),
}

type Stager struct {
	localRoot     string
	sharedRoot    string
	client        *http.Client
	maxBytes      int64
	maxTotalBytes int64
	maxFiles      int
	now           func() time.Time
	staleAfter    time.Duration
	cleanupGrace  time.Duration
	slots         chan struct{}

	mu            sync.Mutex
	cache         map[string]stagedFile
	inflight      map[string]*stageCall
	reservedBytes int64
	reservedFiles int
}

type stagedFile struct {
	localPath  string
	sharedPath string
	createdAt  time.Time
}

type stageCall struct {
	done   chan struct{}
	result stagedFile
	err    error
}

func NewStager(localRoot, sharedRoot string) *Stager {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            safeDialContext(dialer),
		ForceAttemptHTTP2:      true,
		TLSHandshakeTimeout:    15 * time.Second,
		ResponseHeaderTimeout:  30 * time.Second,
		MaxResponseHeaderBytes: 1 << 20,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   defaultTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			req.Header.Del("Referer")
			if len(via) > defaultMaxRedirects {
				return fmt.Errorf("file URL exceeded %d redirects", defaultMaxRedirects)
			}
			return validateRemoteURL(req.URL)
		},
	}
	return &Stager{
		localRoot:     localRoot,
		sharedRoot:    strings.TrimRight(sharedRoot, "/"),
		client:        client,
		maxBytes:      defaultMaxBytes,
		maxTotalBytes: defaultMaxTotalBytes,
		maxFiles:      defaultMaxFiles,
		now:           time.Now,
		staleAfter:    defaultStaleAfter,
		cleanupGrace:  defaultCleanupGrace,
		slots:         make(chan struct{}, defaultMaxConcurrent),
		cache:         make(map[string]stagedFile),
		inflight:      make(map[string]*stageCall),
	}
}

func (s *Stager) Stage(ctx context.Context, source, name string) (string, error) {
	if s == nil || s.client == nil {
		return "", fmt.Errorf("flash file stager is not initialized")
	}
	parsed, err := url.ParseRequestURI(source)
	if err != nil {
		return "", fmt.Errorf("parse file URL: %w", errorWithoutURL(err))
	}
	if err := validateRemoteURL(parsed); err != nil {
		return "", err
	}
	if err := validateFileName(name); err != nil {
		return "", err
	}
	if strings.TrimSpace(s.localRoot) == "" || s.sharedRoot == "" {
		return "", fmt.Errorf("flash staging paths are required")
	}
	cacheKey := source + "\x00" + name
	if cached, call := s.cachedOrBegin(cacheKey); cached != nil {
		return cached.sharedPath, nil
	} else if call != nil {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-call.done:
			return call.result.sharedPath, call.err
		}
	}

	result, err := s.stage(ctx, parsed, name)
	s.finish(cacheKey, result, err)
	return result.sharedPath, err
}

func (s *Stager) stage(ctx context.Context, parsed *url.URL, name string) (stagedFile, error) {
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return stagedFile{}, ctx.Err()
	}
	if err := os.MkdirAll(s.localRoot, 0o755); err != nil {
		return stagedFile{}, fmt.Errorf("create flash staging root: %w", err)
	}
	s.removeStale()
	if err := s.reserveStagingCapacity(); err != nil {
		return stagedFile{}, err
	}
	defer s.releaseStagingCapacity()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return stagedFile{}, fmt.Errorf("create file download request: %w", errorWithoutURL(err))
	}
	req.Header.Set("User-Agent", "Jxh-Go file attachment")
	resp, err := s.client.Do(req)
	if err != nil {
		return stagedFile{}, fmt.Errorf("download file: %w", errorWithoutURL(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return stagedFile{}, fmt.Errorf("download file returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > s.maxBytes {
		return stagedFile{}, fmt.Errorf("file is larger than %d bytes", s.maxBytes)
	}

	dir, err := os.MkdirTemp(s.localRoot, "file-")
	if err != nil {
		return stagedFile{}, fmt.Errorf("create flash staging directory: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(dir)
		}
	}()
	if err := os.Chmod(dir, 0o755); err != nil {
		return stagedFile{}, fmt.Errorf("set flash staging directory permissions: %w", err)
	}
	destination := filepath.Join(dir, name)
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return stagedFile{}, fmt.Errorf("create staged file: %w", err)
	}
	written, copyErr := io.Copy(file, io.LimitReader(resp.Body, s.maxBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		return stagedFile{}, fmt.Errorf("write staged file: %w", copyErr)
	}
	if closeErr != nil {
		return stagedFile{}, fmt.Errorf("close staged file: %w", closeErr)
	}
	if written > s.maxBytes {
		return stagedFile{}, fmt.Errorf("file is larger than %d bytes", s.maxBytes)
	}
	createdAt := s.now()
	if err := os.Chtimes(dir, createdAt, createdAt); err != nil {
		return stagedFile{}, fmt.Errorf("set flash staging directory timestamp: %w", err)
	}
	keep = true
	return stagedFile{
		localPath:  destination,
		sharedPath: path.Join(s.sharedRoot, filepath.Base(dir), name),
		createdAt:  createdAt,
	}, nil
}

func (s *Stager) cachedOrBegin(key string) (*stagedFile, *stageCall) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for cacheKey, cached := range s.cache {
		_, err := os.Stat(cached.localPath)
		if err != nil || !cached.createdAt.Add(s.staleAfter).After(now) {
			delete(s.cache, cacheKey)
		}
	}
	if cached, ok := s.cache[key]; ok {
		return &cached, nil
	}
	if call, ok := s.inflight[key]; ok {
		return nil, call
	}
	s.inflight[key] = &stageCall{done: make(chan struct{})}
	return nil, nil
}

func (s *Stager) finish(key string, result stagedFile, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	call := s.inflight[key]
	call.result = result
	call.err = err
	if err == nil {
		s.cache[key] = result
	}
	delete(s.inflight, key)
	close(call.done)
}

func (s *Stager) reserveStagingCapacity() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	usedBytes, usedFiles, err := stagingUsage(s.localRoot)
	if err != nil {
		return fmt.Errorf("measure flash staging usage: %w", err)
	}
	if usedBytes+s.reservedBytes+s.maxBytes > s.maxTotalBytes || usedFiles+s.reservedFiles+1 > s.maxFiles {
		return fmt.Errorf("flash staging capacity is exhausted")
	}
	s.reservedBytes += s.maxBytes
	s.reservedFiles++
	return nil
}

func (s *Stager) releaseStagingCapacity() {
	s.mu.Lock()
	s.reservedBytes -= s.maxBytes
	s.reservedFiles--
	s.mu.Unlock()
}

func stagingUsage(root string) (int64, int, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, 0, err
	}
	var total int64
	files := 0
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "file-") {
			continue
		}
		err := filepath.WalkDir(filepath.Join(root, entry.Name()), func(_ string, item os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if item.Type().IsRegular() {
				info, err := item.Info()
				if err != nil {
					return err
				}
				total += info.Size()
				files++
			}
			return nil
		})
		if err != nil {
			return 0, 0, err
		}
	}
	return total, files, nil
}

func (s *Stager) removeStale() {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.localRoot)
	if err != nil {
		return
	}
	now := s.now()
	cutoff := now.Add(-s.staleAfter - s.cleanupGrace)
	protectedDirs := make(map[string]struct{}, len(s.cache))
	for key, cached := range s.cache {
		if cached.createdAt.Add(s.staleAfter + s.cleanupGrace).After(now) {
			protectedDirs[filepath.Dir(cached.localPath)] = struct{}{}
		} else {
			delete(s.cache, key)
		}
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "file-") {
			continue
		}
		dir := filepath.Join(s.localRoot, entry.Name())
		if _, protected := protectedDirs[dir]; protected {
			continue
		}
		info, err := entry.Info()
		if err == nil && info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(dir)
		}
	}
}

func errorWithoutURL(err error) error {
	for {
		var urlErr *url.Error
		if !errors.As(err, &urlErr) {
			return err
		}
		err = urlErr.Err
	}
}

func validateRemoteURL(value *url.URL) error {
	if value == nil || (!strings.EqualFold(value.Scheme, "http") && !strings.EqualFold(value.Scheme, "https")) || value.Hostname() == "" {
		return fmt.Errorf("file URL must use http or https and include a host")
	}
	if value.User != nil {
		return fmt.Errorf("file URL credentials are not allowed")
	}
	if port := value.Port(); port != "" && port != "80" && port != "443" {
		return fmt.Errorf("file URL port %q is not allowed", port)
	}
	if ip, err := netip.ParseAddr(value.Hostname()); err == nil && !isPublicIP(ip) {
		return fmt.Errorf("file URL resolves to a non-public address")
	}
	return nil
}

func validateFileName(name string) error {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) ||
		strings.TrimSpace(name) != name || strings.HasSuffix(name, ".") {
		return fmt.Errorf("invalid file name")
	}
	return nil
}

func safeDialContext(dialer *net.Dialer) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("parse download address: %w", err)
		}
		addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("resolve download host: %w", err)
		}
		if len(addresses) == 0 {
			return nil, fmt.Errorf("download host has no address")
		}
		for _, ip := range addresses {
			if !isPublicIP(ip) {
				return nil, fmt.Errorf("download host resolves to a non-public address")
			}
		}
		var dialErrors []error
		for _, ip := range addresses {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			dialErrors = append(dialErrors, err)
		}
		return nil, fmt.Errorf("connect to download host: %w", errors.Join(dialErrors...))
	}
}

func isPublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}
