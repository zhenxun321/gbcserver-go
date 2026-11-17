#!/bin/sh
# 两个服务分别由进程管理器管理，不自动初始化或升级数据库。
set -eu
package_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
executable="$package_dir/gbcserver"
if [ ! -x "$executable" ]; then
    executable="$package_dir/bin/gbcserver"
fi
exec "$executable" plugin-serve --data-dir "$package_dir/gbcserver" "$@"
