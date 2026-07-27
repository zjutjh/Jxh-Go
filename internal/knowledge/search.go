package knowledge

import (
	"fmt"
	"regexp"
	"regexp/syntax"
	"strings"
	"unicode/utf8"

	"github.com/zjutjh/jxh-go/internal/cqreply"
)

const (
	defaultSearchLimit   = 5
	maxSearchLimit       = 10
	maxSearchQueryRunes  = 200
	maxSearchResultRunes = 12000
)

type SearchQuery struct {
	Query string
	Mode  string
	Limit int
}

type SearchResult struct {
	SourceKey string `json:"source_key"`
	Keyword   string `json:"keyword"`
	Path      string `json:"path,omitempty"`
	Category  string `json:"category,omitempty"`
	Answer    string `json:"answer"`
}

func (i *Index) Search(input SearchQuery) ([]SearchResult, error) {
	if i == nil {
		return nil, nil
	}
	query := strings.TrimSpace(input.Query)
	if query == "" {
		return nil, fmt.Errorf("query is empty")
	}
	if utf8.RuneCountInString(query) > maxSearchQueryRunes {
		return nil, fmt.Errorf("query is longer than %d characters", maxSearchQueryRunes)
	}
	limit := input.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}

	match, err := searchMatcher(input.Mode, query)
	if err != nil {
		return nil, err
	}
	results := make([]SearchResult, 0, limit)
	usedRunes := 0
	for _, entry := range i.entries {
		if !entry.Enabled || !entry.AIEnabled || !match(entry.Content) {
			continue
		}
		result := SearchResult{
			SourceKey: entry.SourceKey,
			Keyword:   cqreply.Parse(entry.Keyword).PlainText,
			Path:      cqreply.Parse(entry.Path).PlainText,
			Category:  cqreply.Parse(entry.Category).PlainText,
			Answer:    cqreply.Parse(entry.Answer).PlainText,
		}
		metadataRunes := utf8.RuneCountInString(result.SourceKey + result.Keyword + result.Path + result.Category)
		remaining := maxSearchResultRunes - usedRunes - metadataRunes
		if remaining <= 0 {
			break
		}
		result.Answer = truncateRunes(result.Answer, remaining)
		usedRunes += metadataRunes + utf8.RuneCountInString(result.Answer)
		results = append(results, result)
		if len(results) == limit || usedRunes >= maxSearchResultRunes {
			break
		}
	}
	return results, nil
}

func (r *IndexRef) Search(input SearchQuery) ([]SearchResult, error) {
	if r == nil {
		return nil, nil
	}
	index := r.value.Load()
	if index == nil {
		return nil, nil
	}
	return index.Search(input)
}

func searchMatcher(mode, query string) (func(string) bool, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "and"
	}
	switch mode {
	case "and", "or":
		terms := strings.Fields(strings.ToLower(query))
		return func(content string) bool {
			for _, term := range terms {
				contains := strings.Contains(content, term)
				if mode == "and" && !contains {
					return false
				}
				if mode == "or" && contains {
					return true
				}
			}
			return mode == "and"
		}, nil
	case "regex":
		if err := validateRegexPattern(query); err != nil {
			return nil, err
		}
		re, err := regexp.Compile("(?i)" + query)
		if err != nil {
			return nil, fmt.Errorf("invalid regular expression: %w", err)
		}
		return re.MatchString, nil
	default:
		return nil, fmt.Errorf("unsupported search mode %q", mode)
	}
}

const (
	maxRegexPatternRunes = 120
	// 一条正则至少要"钉住"这么多个具体字符才算有选择性。
	minRegexSpecificity = 2
	// 字符类窄到这个规模才算钉住一个字符：[菜单] 算，[^\x00] 和 \w 不算。
	maxNarrowClassRunes = 40
)

// validateRegexPattern 要求正则具备选择性。Go 使用 RE2，不存在灾难性回溯，
// 所以这里防的不是 CPU 开销，而是用一条无差别匹配的正则把整个知识库拖走。
//
// 判定方式是解析出 AST 后计算"必须匹配的具体字符数"，而不是维护通配写法黑名单。
// 黑名单挡不住 [^\x00]{1,}、[\s\S]* 这类等价写法，也挡不住 菜单|.* 这种
// 用一个合法分支掩护全匹配分支的写法，同时还会误伤 宿舍.*费用 这类正常查询。
func validateRegexPattern(pattern string) error {
	if utf8.RuneCountInString(pattern) > maxRegexPatternRunes {
		return fmt.Errorf("正则表达式过长，最多 %d 个字符", maxRegexPatternRunes)
	}
	// 与 searchMatcher 的编译保持一致，让 AST 反映实际生效的模式。
	parsed, err := syntax.Parse("(?i)"+pattern, syntax.Perl)
	if err != nil {
		return fmt.Errorf("invalid regular expression: %w", err)
	}
	if regexSpecificity(parsed) < minRegexSpecificity {
		return fmt.Errorf("正则表达式缺少具体的搜索词，请确保每个分支都固定匹配 %d 个以上字符，或改用 and / or 模式", minRegexSpecificity)
	}
	return nil
}

// regexSpecificity 返回该模式匹配成功时必然被固定下来的字符个数。
// 数值越低说明匹配面越宽，0 表示可以匹配任意内容。
func regexSpecificity(re *syntax.Regexp) int {
	switch re.Op {
	case syntax.OpLiteral:
		return len(re.Rune)
	case syntax.OpCharClass:
		if classRuneCount(re.Rune) <= maxNarrowClassRunes {
			return 1
		}
		return 0
	case syntax.OpCapture:
		return regexSpecificity(re.Sub[0])
	case syntax.OpConcat:
		// 拼接项全都要命中，任一项提供的约束都能收窄结果。
		total := 0
		for _, sub := range re.Sub {
			total += regexSpecificity(sub)
		}
		return total
	case syntax.OpAlternate:
		// 任一分支命中即可，因此整体选择性取决于最宽的那个分支。
		weakest := -1
		for _, sub := range re.Sub {
			score := regexSpecificity(sub)
			if weakest < 0 || score < weakest {
				weakest = score
			}
		}
		if weakest < 0 {
			return 0
		}
		return weakest
	case syntax.OpPlus:
		return regexSpecificity(re.Sub[0])
	case syntax.OpRepeat:
		if re.Min < 1 {
			return 0
		}
		return regexSpecificity(re.Sub[0]) * re.Min
	case syntax.OpStar, syntax.OpQuest:
		// 可以匹配零次，不提供任何约束。
		return 0
	case syntax.OpNoMatch:
		// 永不命中，不会泄露数据。
		return minRegexSpecificity
	default:
		// OpAnyChar、OpAnyCharNotNL、OpEmptyMatch 和各类锚点都不限制内容。
		return 0
	}
}

// classRuneCount 统计字符类覆盖的码点数量，入参是 [lo, hi] 成对的区间表。
func classRuneCount(ranges []rune) int {
	total := 0
	for i := 0; i+1 < len(ranges); i += 2 {
		total += int(ranges[i+1]-ranges[i]) + 1
		if total > maxNarrowClassRunes {
			return total
		}
	}
	return total
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
