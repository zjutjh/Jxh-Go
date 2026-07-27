#!/bin/sh
set -e

if [ "$(id -u)" = "0" ]; then
	TARGET_UID="${PUID:-10001}"
	TARGET_GID="${PGID:-10001}"

	case "${TARGET_UID}" in
		''|*[!0-9]*) echo "PUID must be a positive integer" >&2; exit 1 ;;
	esac
	case "${TARGET_GID}" in
		''|*[!0-9]*) echo "PGID must be a positive integer" >&2; exit 1 ;;
	esac
	if [ "${TARGET_UID}" -le 0 ] || [ "${TARGET_GID}" -le 0 ]; then
		echo "PUID and PGID must be positive integers" >&2
		exit 1
	fi

	CURRENT_UID=$(id -u appuser)
	CURRENT_GID=$(id -g appuser)

	if [ "${TARGET_GID}" != "${CURRENT_GID}" ]; then
		groupmod -o -g "${TARGET_GID}" appuser
	fi
	if [ "${TARGET_UID}" != "${CURRENT_UID}" ] || [ "${TARGET_GID}" != "${CURRENT_GID}" ]; then
		usermod -o -u "${TARGET_UID}" -g "${TARGET_GID}" appuser
	fi

	mkdir -p /app/data/cache /app/data/exports /app/data/flash
	chown -R appuser:appuser /app/data/cache /app/data/exports /app/data/flash
	chmod 0755 /app/data/flash
	exec gosu appuser "$@"
fi

exec "$@"
