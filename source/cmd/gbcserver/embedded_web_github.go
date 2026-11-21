package main

import "embed"

//go:embed web/admin.html web/login.html web/ping.html web/admin.css web/admin-config.js web/admin-legacy.js web/admin-tokens.js web/player.js web/status-page.js web/theme.js web/interaction-effects.js web/i18n.js web/i18n-player.js web/i18n-admin.js web/minecraft-items.json web/system-redesign.css web/material.css web/shell-layout.css web/interaction-effects.css web/theme.css web/visual-polish.css web/status-redesign.css web/vendor/fontawesome/css/fontawesome.min.css web/vendor/fontawesome/css/solid.min.css web/vendor/fontawesome/webfonts/fa-solid-900.woff2 web/vendor/fontawesome/webfonts/fa-solid-900.ttf web/vendor/fontawesome/LICENSE.txt web/images/logo.png web/images/default_item.png
var embeddedWebFiles embed.FS

const githubReleaseBuild = true
