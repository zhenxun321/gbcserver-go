package main

import "embed"

//go:embed web/admin.html web/login.html web/ping.html web/admin.css web/admin-config.js web/admin-legacy.js web/admin-tokens.js web/player.js web/status-page.js web/theme.js web/site-config.js web/interaction-effects.js web/minecraft-items.json web/system-redesign.css web/interaction-effects.css web/theme.css web/visual-polish.css web/status-redesign.css web/images/logo.png web/images/default_item.png
var embeddedWebFiles embed.FS

const githubReleaseBuild = true
