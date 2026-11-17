#!/bin/sh
# 固定配置位置，同时保留调用方传入的每一个参数。
set -eu
package_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
executable="$package_dir/gbcserver"
if [ ! -x "$executable" ]; then
    executable="$package_dir/bin/gbcserver"
fi
exec "$executable" web-serve --data-dir "$package_dir/gbcserver" "$@"
