#!/bin/sh
set -eu

# The browser receives this value, so allow only an absolute HTTP(S) origin.
# This also keeps the value safe for both a JavaScript string and an nginx CSP
# source expression. Paths, credentials, queries, and fragments are forbidden.
api_url="${CLUMOOVE_API_URL:-}"
origin_pattern='^https?://([A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?|\[[0-9A-Fa-f:]+\])(:[0-9]{1,5})?$'

if [ -n "$api_url" ]; then
    if ! printf '%s\n' "$api_url" | grep -Eq "$origin_pattern"; then
        echo >&2 "CLUMOOVE_API_URL must be an origin-only http(s) URL"
        exit 1
    fi

    api_port=$(printf '%s\n' "$api_url" | sed -n 's/.*:\([0-9][0-9]*\)$/\1/p')
    if [ -n "$api_port" ]; then
        # Normalize before numeric comparisons: some shells treat a leading zero as
        # octal, while URLs such as :00080 are syntactically allowed above.
        while [ "${api_port#0}" != "$api_port" ]; do
            api_port=${api_port#0}
        done
        api_port=${api_port:-0}
        if [ "$api_port" -lt 1 ] || [ "$api_port" -gt 65535 ]; then
            echo >&2 "CLUMOOVE_API_URL port must be between 1 and 65535"
            exit 1
        fi
    fi

    export CLUMOOVE_API_CSP_SOURCE=" $api_url"
else
    export CLUMOOVE_API_CSP_SOURCE=''
fi

# The official 20-envsubst-on-templates.sh hook has already written the
# baseline config. Regenerate it after validation so the validated source is
# present in the CSP used by nginx itself. Add any future template variables to
# this explicit envsubst list as well.
envsubst '${CLUMOOVE_API_CSP_SOURCE}' \
    < /etc/nginx/templates/default.conf.template \
    > /etc/nginx/conf.d/default.conf

printf 'window.__CLUMOOVE_RUNTIME_CONFIG__ = Object.freeze({ apiUrl: "%s" });\n' "$api_url" \
    > /usr/share/nginx/html/runtime-config.js
