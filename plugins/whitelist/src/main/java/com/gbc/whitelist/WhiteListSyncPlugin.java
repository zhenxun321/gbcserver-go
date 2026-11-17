package com.gbc.whitelist;

import org.bukkit.Bukkit;
import org.bukkit.event.EventHandler;
import org.bukkit.event.EventPriority;
import org.bukkit.event.Listener;
import org.bukkit.event.player.AsyncPlayerPreLoginEvent;
import org.bukkit.event.player.PlayerJoinEvent;
import org.bukkit.event.player.PlayerQuitEvent;
import org.bukkit.plugin.java.JavaPlugin;
import org.bukkit.entity.Player;
import org.json.JSONObject;
import org.json.JSONArray;

import java.io.IOException;
import java.net.InetAddress;
import java.net.URI;
import java.net.URLEncoder;
import java.net.UnknownHostException;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.ConcurrentHashMap;

public final class WhiteListSyncPlugin extends JavaPlugin implements Listener {
    private HttpClient httpClient;
    private URI apiBase;
    private String whitelistToken;
    private Duration requestTimeout;
    private final Map<UUID, PendingLogin> pendingAttempts = new ConcurrentHashMap<>();

    @Override
    public void onEnable() {
        saveDefaultConfig();
        reloadConfig();

        try {
            apiBase = validatePrivateApiUrl(getConfig().getString("api-url", "http://127.0.0.1:18094"));
        } catch (IllegalArgumentException exception) {
            getLogger().severe(exception.getMessage());
            getServer().getPluginManager().disablePlugin(this);
            return;
        }

        whitelistToken = getConfig().getString("whitelist-token", "").trim();
        if (whitelistToken.isBlank() || "CHANGE_ME".equalsIgnoreCase(whitelistToken)) {
            getLogger().severe("必须配置独立的 whitelist scope token。");
            getServer().getPluginManager().disablePlugin(this);
            return;
        }
        long timeoutSeconds = Math.max(1, Math.min(10, getConfig().getLong("request-timeout-seconds", 3)));
        requestTimeout = Duration.ofSeconds(timeoutSeconds);
        httpClient = HttpClient.newBuilder()
                .connectTimeout(Duration.ofSeconds(Math.min(2, timeoutSeconds)))
                .followRedirects(HttpClient.Redirect.NEVER)
                .build();
        getServer().getPluginManager().registerEvents(this, this);
        Bukkit.getScheduler().runTaskTimer(this, this::scheduleHeartbeat, 20L * 30, 20L * 30);
        getLogger().info("内网白名单插件已启用。");
    }

    @EventHandler(priority = EventPriority.LOWEST)
    public void onAsyncPreLogin(AsyncPlayerPreLoginEvent event) {
        // 若服务端异常地在主线程触发该事件，也拒绝登录，绝不在主线程等待网络。
        if (!event.isAsynchronous()) {
            pendingAttempts.remove(event.getUniqueId());
            event.disallow(AsyncPlayerPreLoginEvent.Result.KICK_OTHER, "白名单服务不可用，请稍后重试。");
            getLogger().severe("AsyncPlayerPreLoginEvent 在同步线程触发，已按 fail-closed 拒绝登录。");
            return;
        }

        try {
            long now = System.currentTimeMillis();
            pendingAttempts.entrySet().removeIf(entry -> entry.getValue().expiresAt <= now);
            String encodedName = URLEncoder.encode(event.getName(), StandardCharsets.UTF_8).replace("+", "%20");
            URI endpoint = apiBase.resolve("/check/" + encodedName);
            HttpRequest request = HttpRequest.newBuilder(endpoint)
                    .timeout(requestTimeout)
                    .header("Authorization", "Bearer " + whitelistToken)
                    .header("Accept", "application/json")
                    .POST(HttpRequest.BodyPublishers.noBody())
                    .build();
            HttpResponse<String> response = httpClient.send(request, HttpResponse.BodyHandlers.ofString(StandardCharsets.UTF_8));
            if (response.statusCode() != 200) {
                pendingAttempts.remove(event.getUniqueId());
                event.disallow(AsyncPlayerPreLoginEvent.Result.KICK_OTHER, loginDeniedMessage(response));
                return;
            }
            JSONObject body = new JSONObject(response.body());
            if (!body.optBoolean("allowed", false)) {
                pendingAttempts.remove(event.getUniqueId());
                event.disallow(AsyncPlayerPreLoginEvent.Result.KICK_OTHER, loginDeniedMessage(response));
                return;
            }
            String attemptId = body.optString("loginAttemptId", "").trim();
            long expiresAt = body.optLong("expiresAt", 0L);
            if (attemptId.isBlank() || expiresAt <= System.currentTimeMillis()) {
                pendingAttempts.remove(event.getUniqueId());
                event.disallow(AsyncPlayerPreLoginEvent.Result.KICK_OTHER, "白名单服务响应无效，请稍后重试。");
                return;
            }
            pendingAttempts.put(event.getUniqueId(), new PendingLogin(event.getName(), attemptId, expiresAt));
        } catch (InterruptedException exception) {
            Thread.currentThread().interrupt();
            pendingAttempts.remove(event.getUniqueId());
            event.disallow(AsyncPlayerPreLoginEvent.Result.KICK_OTHER, "白名单服务不可用，请稍后重试。");
        } catch (IOException | RuntimeException exception) {
            pendingAttempts.remove(event.getUniqueId());
            event.disallow(AsyncPlayerPreLoginEvent.Result.KICK_OTHER, "白名单服务不可用，请稍后重试。");
            getLogger().warning("白名单检查失败，已拒绝玩家 " + event.getName() + " 登录。");
        }
    }

    @EventHandler(priority = EventPriority.MONITOR)
    public void onPlayerJoin(PlayerJoinEvent event) {
        Player player = event.getPlayer();
        UUID playerId = player.getUniqueId();
        PendingLogin pending = pendingAttempts.remove(playerId);
        if (pending == null || pending.expiresAt <= System.currentTimeMillis()) {
            player.kickPlayer("登录确认已失效，请重新连接。");
            return;
        }
        Bukkit.getScheduler().runTaskAsynchronously(this, () -> {
            boolean confirmed = confirmJoin(pending);
            Bukkit.getScheduler().runTask(this, () -> {
                Player current = Bukkit.getPlayer(playerId);
                if (!confirmed) {
                    if (current != null) {
                        current.kickPlayer("白名单服务未能确认登录，请稍后重试。");
                    }
                    return;
                }
                // 玩家可能在确认请求返回前离开；再次登出可收敛请求竞态。
                if (current == null) {
                    Bukkit.getScheduler().runTaskAsynchronously(this, () -> sendLogout(pending.playerName));
                }
            });
        });
    }

    @EventHandler(priority = EventPriority.MONITOR)
    public void onPlayerQuit(PlayerQuitEvent event) {
        String playerName = event.getPlayer().getName();
        pendingAttempts.remove(event.getPlayer().getUniqueId());
        Bukkit.getScheduler().runTaskAsynchronously(this, () -> {
            sendLogout(playerName);
        });
    }

    private boolean confirmJoin(PendingLogin pending) {
        try {
            String encodedName = URLEncoder.encode(pending.playerName, StandardCharsets.UTF_8).replace("+", "%20");
            URI endpoint = apiBase.resolve("/join/" + encodedName);
            for (int attempt = 0; attempt < 3 && System.currentTimeMillis() < pending.expiresAt; attempt++) {
                try {
                    HttpRequest request = HttpRequest.newBuilder(endpoint)
                            .timeout(requestTimeout)
                            .header("Authorization", "Bearer " + whitelistToken)
                            .header("X-Login-Attempt-ID", pending.attemptId)
                            .header("Accept", "application/json")
                            .POST(HttpRequest.BodyPublishers.noBody())
                            .build();
                    HttpResponse<String> response = httpClient.send(request, HttpResponse.BodyHandlers.ofString(StandardCharsets.UTF_8));
                    if (response.statusCode() == 200 && new JSONObject(response.body()).optBoolean("ok", false)) {
                        return true;
                    }
                    if (response.statusCode() != 408 && response.statusCode() != 425 && response.statusCode() < 500) {
                        return false;
                    }
                } catch (IOException exception) {
                    if (attempt == 2) {
                        throw exception;
                    }
                }
                Thread.sleep(200L * (attempt + 1));
            }
        } catch (InterruptedException exception) {
            Thread.currentThread().interrupt();
        } catch (IOException | RuntimeException exception) {
            getLogger().warning("确认玩家 " + pending.playerName + " 入服失败，将按 fail-closed 处理。");
        }
        return false;
    }

    private void sendLogout(String playerName) {
        try {
            String encodedName = URLEncoder.encode(playerName, StandardCharsets.UTF_8).replace("+", "%20");
            URI endpoint = apiBase.resolve("/logout/" + encodedName);
            HttpRequest request = HttpRequest.newBuilder(endpoint)
                    .timeout(requestTimeout)
                    .header("Authorization", "Bearer " + whitelistToken)
                    .header("Accept", "application/json")
                    .POST(HttpRequest.BodyPublishers.noBody())
                    .build();
            HttpResponse<Void> response = httpClient.send(request, HttpResponse.BodyHandlers.discarding());
            if (response.statusCode() != 200) {
                getLogger().warning("白名单服务未确认玩家 " + playerName + " 的离线状态，HTTP " + response.statusCode() + "。");
            }
        } catch (InterruptedException exception) {
            Thread.currentThread().interrupt();
            getLogger().warning("上报玩家 " + playerName + " 离线状态时被中断。");
        } catch (IOException | RuntimeException exception) {
            getLogger().warning("上报玩家 " + playerName + " 离线状态失败。");
        }
    }

    private void scheduleHeartbeat() {
        List<OnlinePlayer> online = new ArrayList<>();
        for (Player player : Bukkit.getOnlinePlayers()) {
            online.add(new OnlinePlayer(player.getUniqueId(), player.getName()));
        }
        Bukkit.getScheduler().runTaskAsynchronously(this, () -> sendHeartbeat(online));
    }

    private void sendHeartbeat(List<OnlinePlayer> online) {
        JSONArray names = new JSONArray();
        for (OnlinePlayer player : online) {
            names.put(player.name);
        }
        try {
            URI endpoint = apiBase.resolve("/heartbeat");
            HttpRequest request = HttpRequest.newBuilder(endpoint)
                    .timeout(requestTimeout)
                    .header("Authorization", "Bearer " + whitelistToken)
                    .header("Content-Type", "application/json")
                    .header("Accept", "application/json")
                    .POST(HttpRequest.BodyPublishers.ofString(new JSONObject().put("players", names).toString(), StandardCharsets.UTF_8))
                    .build();
            HttpResponse<String> response = httpClient.send(request, HttpResponse.BodyHandlers.ofString(StandardCharsets.UTF_8));
            if (response.statusCode() != 200) {
                getLogger().warning("在线心跳未获服务确认，HTTP " + response.statusCode() + "。");
                return;
            }
            JSONArray rejected = new JSONObject(response.body()).optJSONArray("rejectedPlayers");
            if (rejected == null || rejected.length() == 0) {
                return;
            }
            List<UUID> kickIds = new ArrayList<>();
            for (OnlinePlayer player : online) {
                for (int index = 0; index < rejected.length(); index++) {
                    if (player.name.equalsIgnoreCase(rejected.optString(index))) {
                        kickIds.add(player.playerId);
                        break;
                    }
                }
            }
            Bukkit.getScheduler().runTask(this, () -> {
                for (UUID playerId : kickIds) {
                    Player player = Bukkit.getPlayer(playerId);
                    if (player != null) {
                        player.kickPlayer("账号状态已变更，请重新登录。");
                    }
                }
            });
        } catch (InterruptedException exception) {
            Thread.currentThread().interrupt();
            getLogger().warning("在线心跳请求被中断。");
        } catch (IOException | RuntimeException exception) {
            getLogger().warning("在线心跳失败；服务端租约到期后会停止累计在线时长。");
        }
    }

    private String loginDeniedMessage(HttpResponse<String> response) {
        try {
            JSONObject body = new JSONObject(response.body());
            String reason = body.optString("reason", "");
            if ("not_authorized".equals(reason)) {
                return "登录失败：请先在网页端确认登录授权。";
            }
            if ("account_inactive".equals(reason)) {
                return "登录失败：账号未激活，请联系管理员。";
            }
            if ("account_banned".equals(reason)) {
                return "登录失败：账号已被封禁。";
            }
        } catch (RuntimeException ignored) {
            // 非 JSON 错误体不能变成登录许可。
        }
        return "登录失败：白名单服务拒绝或暂不可用。";
    }

    private URI validatePrivateApiUrl(String value) {
        try {
            URI uri = URI.create(value == null ? "" : value.trim());
            String scheme = uri.getScheme();
            String host = uri.getHost();
            if (host == null || uri.getUserInfo() != null || uri.getQuery() != null || uri.getFragment() != null) {
                throw new IllegalArgumentException("api-url 配置不合法。");
            }
            if (!"http".equalsIgnoreCase(scheme) && !"https".equalsIgnoreCase(scheme)) {
                throw new IllegalArgumentException("api-url 只支持 HTTP 或 HTTPS。");
            }

            // 仅允许 IP 字面地址，避免每次请求重新解析域名时发生 DNS 重绑定。
            String addressText = host;
            if (addressText.startsWith("[") && addressText.endsWith("]")) {
                addressText = addressText.substring(1, addressText.length() - 1);
            }
            boolean ipv4Literal = isIPv4Literal(addressText);
            boolean ipv6Literal = isIPv6Literal(addressText);
            if (!ipv4Literal && !ipv6Literal) {
                throw new IllegalArgumentException("白名单插件 api-url 必须使用规范的私网 IP 字面地址，不能使用域名。");
            }

            InetAddress address = InetAddress.getByName(addressText);
            if (!isPrivateAddress(address)) {
                throw new IllegalArgumentException("白名单插件 api-url 必须是 loopback、RFC1918、IPv6 ULA 或 link-local 地址。");
            }
            return uri;
        } catch (UnknownHostException | IllegalArgumentException exception) {
            throw new IllegalArgumentException("白名单插件 api-url 必须是有效的私网 IP 地址。", exception);
        }
    }

    private boolean isIPv4Literal(String value) {
        String[] octets = value.split("\\.", -1);
        if (octets.length != 4) {
            return false;
        }
        for (String octet : octets) {
            if (octet.isEmpty() || (octet.length() > 1 && octet.startsWith("0"))) {
                return false;
            }
            try {
                int parsed = Integer.parseInt(octet);
                if (parsed < 0 || parsed > 255) {
                    return false;
                }
            } catch (NumberFormatException exception) {
                return false;
            }
        }
        return true;
    }

    private boolean isIPv6Literal(String value) {
        if (value.indexOf(':') < 0 || !value.matches("[0-9a-fA-F:]+")) {
            return false;
        }
        int compression = value.indexOf("::");
        if (compression >= 0 && value.indexOf("::", compression + 1) >= 0) {
            return false;
        }
        String[] groups = value.split(":", -1);
        int groupCount = 0;
        for (String group : groups) {
            if (group.isEmpty()) {
                continue;
            }
            if (group.length() > 4) {
                return false;
            }
            groupCount++;
        }
        if (compression < 0) {
            return groups.length == 8 && groupCount == 8;
        }
        return groupCount < 8;
    }

    private boolean isPrivateAddress(InetAddress address) {
        if (address.isLoopbackAddress() || address.isLinkLocalAddress()) {
            return true;
        }
        byte[] bytes = address.getAddress();
        if (bytes.length == 4) {
            int first = bytes[0] & 0xff;
            int second = bytes[1] & 0xff;
            return first == 10
                    || (first == 172 && second >= 16 && second <= 31)
                    || (first == 192 && second == 168);
        }
        if (bytes.length == 16) {
            // IPv6 ULA 地址属于 fc00::/7。
            return (bytes[0] & 0xfe) == 0xfc;
        }
        return false;
    }

    private static final class PendingLogin {
        private final String playerName;
        private final String attemptId;
        private final long expiresAt;

        private PendingLogin(String playerName, String attemptId, long expiresAt) {
            this.playerName = playerName;
            this.attemptId = attemptId;
            this.expiresAt = expiresAt;
        }
    }

    private static final class OnlinePlayer {
        private final UUID playerId;
        private final String name;

        private OnlinePlayer(UUID playerId, String name) {
            this.playerId = playerId;
            this.name = name;
        }
    }
}

