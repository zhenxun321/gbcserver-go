#!/bin/sh
# 一体启动 Web/API、白名单 listener 和兑换 listener；插件 JAR 仍由 Leaves/Paper 加载。
set -eu
package_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
executable="$package_dir/gbcserver"
if [ ! -x "$executable" ]; then
    executable="$package_dir/bin/gbcserver"
fi
data_dir="$package_dir/gbcserver"
exec "$executable" start --background --data-dir "$data_dir" "$@"
