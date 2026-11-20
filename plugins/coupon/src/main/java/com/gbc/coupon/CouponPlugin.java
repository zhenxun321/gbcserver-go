package com.gbc.coupon;

import org.bukkit.Bukkit;
import org.bukkit.Material;
import org.bukkit.command.Command;
import org.bukkit.command.CommandSender;
import org.bukkit.command.PluginCommand;
import org.bukkit.configuration.ConfigurationSection;
import org.bukkit.configuration.file.FileConfiguration;
import org.bukkit.configuration.file.YamlConfiguration;
import org.bukkit.entity.Player;
import org.bukkit.event.EventHandler;
import org.bukkit.event.Listener;
import org.bukkit.event.player.PlayerJoinEvent;
import org.bukkit.inventory.ItemStack;
import org.bukkit.plugin.java.JavaPlugin;
import org.json.JSONArray;
import org.json.JSONObject;

import java.io.File;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.InetAddress;
import java.net.URI;
import java.net.URISyntaxException;
import java.net.URL;
import java.net.UnknownHostException;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardCopyOption;
import java.nio.file.StandardOpenOption;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import java.util.concurrent.ConcurrentHashMap;
import java.util.logging.Level;

public class CouponPlugin extends JavaPlugin implements Listener {
    private static final int MAX_RESPONSE_BYTES = 64 * 1024;
    private static final int MAX_AUTOMATIC_RETRIES = 3;
    private final Set<UUID> pendingRequests = ConcurrentHashMap.newKeySet();
    private String apiUrl;
    private String serverToken;
    private int maxItemAmount;
    private File deliveryLedgerFile;
    private FileConfiguration deliveryLedger;

    @Override
    public void onEnable() {
        saveDefaultConfig();
        reloadConfig();

        FileConfiguration config = getConfig();
        deliveryLedgerFile = new File(getDataFolder(), "pending-deliveries.yml");
        deliveryLedger = deliveryLedgerFile.isFile()
                ? YamlConfiguration.loadConfiguration(deliveryLedgerFile)
                : new YamlConfiguration();
        apiUrl = trimTrailingQuestionMark(
                config.getString("api-url", "http://127.0.0.1:18095/api"));
        // 只读取带 coupon.redeem 权限的独立凭据。
        serverToken = config.getString("coupon-token", "").trim();
        maxItemAmount = Math.max(1, config.getInt("max-item-amount", 2304));

        if (serverToken.isBlank() || isPlaceholderToken(serverToken)) {
            getLogger().warning("coupon-token is not configured; coupon redemption will be rejected.");
        }
        try {
            validateApiUrl(apiUrl);
        } catch (IllegalArgumentException exception) {
            getLogger().severe(exception.getMessage());
            getServer().getPluginManager().disablePlugin(this);
            return;
        }

        PluginCommand command = getCommand("coupon");
        if (command == null) {
            getLogger().severe("Command 'coupon' is missing from plugin.yml.");
            getServer().getPluginManager().disablePlugin(this);
            return;
        }
        command.setExecutor(this);
        getServer().getPluginManager().registerEvents(this, this);
        getLogger().info("Coupon plugin enabled.");
    }

    @EventHandler
    public void onPlayerJoin(PlayerJoinEvent event) {
        UUID playerId = event.getPlayer().getUniqueId();
        Bukkit.getScheduler().runTask(this, () -> {
            Player player = Bukkit.getPlayer(playerId);
            if (player != null) {
                deliverPendingForPlayer(player);
            }
        });
    }

    @Override
    public boolean onCommand(CommandSender sender, Command command, String label, String[] args) {
        if (!(sender instanceof Player player)) {
            sender.sendMessage("Only players can use this command.");
            return true;
        }
        if (args.length != 1 || args[0].isBlank()) {
            player.sendMessage("Usage: /coupon <code>");
            return true;
        }
        if (serverToken.isBlank() || isPlaceholderToken(serverToken)) {
            player.sendMessage("Coupon service is not configured.");
            return true;
        }

        UUID playerId = player.getUniqueId();
        if (hasPendingDeliveries(playerId)) {
            player.sendMessage("A saved coupon reward is waiting. Retrying delivery now.");
            deliverPendingForPlayer(player);
            return true;
        }
        if (!pendingRequests.add(playerId)) {
            player.sendMessage("A coupon request is already being processed.");
            return true;
        }

        String currentPlayerName = player.getName();
        String couponCode = args[0];
        PendingRequest pendingRequest = getOrCreatePendingRequest(playerId, currentPlayerName, couponCode);
        if (pendingRequest.requestId().isBlank()) {
            pendingRequests.remove(playerId);
            player.sendMessage("Coupon request could not be saved safely. Please try again later.");
            return true;
        }
        String playerName = pendingRequest.playerName();
        String requestId = pendingRequest.requestId();
        String redemptionPath = pendingRequestPath(playerId, couponCode);
        player.sendMessage("Validating coupon...");

        Bukkit.getScheduler().runTaskAsynchronously(this, () -> {
            try {
                CouponResponse response = requestCouponWithRetries(playerName, couponCode, requestId);
                Bukkit.getScheduler().runTask(this, () -> {
                    pendingRequests.remove(playerId);
                    if (response.success()) {
                        // 先持久化完整奖励，再触碰背包，重启后才能自动恢复。
                        if (!savePendingDelivery(playerId, playerName, requestId, redemptionPath, response.rewards())) {
                            if (player.isOnline()) {
                                player.sendMessage("Coupon was accepted, but its reward receipt could not be saved. Please retry after fixing server storage.");
                            }
                            return;
                        }
                        if (player.isOnline()) {
                            deliverPendingForPlayer(player);
                        }
                    } else {
                        if (player.isOnline()) {
                            player.sendMessage("Coupon failed: " + response.message());
                        }
                        if (!response.retryable()) {
                            clearPendingRequestPath(redemptionPath);
                        }
                    }
                });
            } catch (Exception exception) {
                getLogger().log(Level.WARNING, "Coupon request failed for " + playerName, exception);
                Bukkit.getScheduler().runTask(this, () -> {
                    pendingRequests.remove(playerId);
                    if (player.isOnline()) {
                        player.sendMessage("Coupon request failed. Please try again later.");
                    }
                });
            }
        });
        return true;
    }

    private CouponResponse requestCouponWithRetries(String playerName, String couponCode, String requestId) {
        CouponResponse lastResponse = CouponResponse.failure("Coupon service is temporarily unavailable.", true);
        for (int attempt = 0; attempt <= MAX_AUTOMATIC_RETRIES; attempt++) {
            try {
                lastResponse = requestCoupon(playerName, couponCode, requestId);
                if (!lastResponse.retryable()) {
                    return lastResponse;
                }
            } catch (IOException | RuntimeException exception) {
                getLogger().warning("Coupon request attempt " + (attempt + 1) + " failed; the same request ID will be retried.");
                lastResponse = CouponResponse.failure("Coupon service is temporarily unavailable.", true);
            }

            if (attempt == MAX_AUTOMATIC_RETRIES) {
                return lastResponse;
            }
            try {
                Thread.sleep(1000L << attempt);
            } catch (InterruptedException exception) {
                Thread.currentThread().interrupt();
                return CouponResponse.failure("Coupon retry was interrupted; run the command again to resume.", true);
            }
        }
        return lastResponse;
    }

    private CouponResponse requestCoupon(String playerName, String couponCode, String requestId) throws IOException {
        HttpURLConnection connection = null;
        try {
            URL url = URI.create(apiUrl).toURL();
            connection = (HttpURLConnection) url.openConnection();
            connection.setRequestMethod("POST");
            connection.setConnectTimeout(8000);
            connection.setReadTimeout(8000);
            connection.setInstanceFollowRedirects(false);
            connection.setDoOutput(true);
            connection.setRequestProperty("Accept", "application/json");
            connection.setRequestProperty("Content-Type", "application/json; charset=UTF-8");
            connection.setRequestProperty("Authorization", "Bearer " + serverToken);
            connection.setRequestProperty("X-Request-ID", requestId);

            byte[] requestBody = new JSONObject()
                    .put("method", "useCoupon")
                    .put("username", playerName)
                    .put("code", couponCode)
                    .put("requestId", requestId)
                    .toString()
                    .getBytes(StandardCharsets.UTF_8);
            connection.setFixedLengthStreamingMode(requestBody.length);
            try (OutputStream output = connection.getOutputStream()) {
                output.write(requestBody);
            }

            int responseCode = connection.getResponseCode();
            InputStream stream = responseCode >= 200 && responseCode < 300
                    ? connection.getInputStream()
                    : connection.getErrorStream();
            String responseBody = stream == null ? "" : readBody(stream);

            if (responseCode != HttpURLConnection.HTTP_OK) {
                getLogger().warning("Coupon API returned HTTP " + responseCode);
                return CouponResponse.failure("Service response: HTTP " + responseCode, isRetryableStatus(responseCode));
            }

            JSONObject json = new JSONObject(responseBody);
            if (!json.optBoolean("success", false)) {
                return CouponResponse.failure(json.optString("message", "Coupon rejected."), false);
            }

            List<ItemReward> rewards = new ArrayList<>();
            JSONArray items = json.optJSONArray("items");
            if (items != null) {
                for (int index = 0; index < items.length(); index++) {
                    JSONObject item = items.optJSONObject(index);
                    if (item == null) {
                        continue;
                    }
                    rewards.add(new ItemReward(
                            item.optString("itemId", ""),
                            item.optInt("amount", 0)));
                }
            }
            return CouponResponse.success(rewards);
        } finally {
            if (connection != null) {
                connection.disconnect();
            }
        }
    }

    private boolean deliverRewards(Player player, List<ItemReward> rewards, String couponCode) {
        if (rewards == null || rewards.isEmpty()) {
            player.sendMessage("Coupon reward receipt is empty; it has been kept for retry.");
            return false;
        }

        List<ItemStack> preparedItems = new ArrayList<>();
        int receivedAmount = 0;
        for (ItemReward reward : rewards) {
            ItemStack itemStack = createItemStack(reward.itemId(), reward.amount());
            if (itemStack == null) {
                player.sendMessage("Coupon rewards contain an invalid item. The receipt is kept for retry after the item configuration is fixed.");
                return false;
            }
            preparedItems.add(itemStack);
            receivedAmount += reward.amount();
        }

        for (ItemStack itemStack : preparedItems) {
            Map<Integer, ItemStack> leftovers = player.getInventory().addItem(itemStack);
            leftovers.values().forEach(item ->
                    player.getWorld().dropItemNaturally(player.getLocation(), item));
        }
        // 先让 Bukkit 保存背包，再由调用方清除待发账本。
        player.saveData();

        player.sendMessage("Coupon reward delivered: " + preparedItems.size()
                + " item type(s), " + receivedAmount + " item(s).");
        if (couponCode != null && !couponCode.isBlank()) {
            player.sendMessage("Code: " + couponCode);
        }
        return true;
    }

    private boolean savePendingDelivery(UUID playerId, String playerName, String requestId,
                                        String redemptionPath, List<ItemReward> rewards) {
        String path = pendingDeliveryPath(playerId, requestId);
        deliveryLedger.set(path + ".player-name", playerName);
        deliveryLedger.set(path + ".redemption-path", redemptionPath);
        List<Map<String, Object>> serialized = new ArrayList<>();
        for (ItemReward reward : rewards) {
            serialized.add(Map.of("itemId", reward.itemId(), "amount", reward.amount()));
        }
        deliveryLedger.set(path + ".items", serialized);
        if (persistDeliveryLedger()) {
            return true;
        }
        deliveryLedger.set(path, null);
        return false;
    }

    private boolean hasPendingDeliveries(UUID playerId) {
        ConfigurationSection section = deliveryLedger.getConfigurationSection("pending-deliveries." + playerId);
        return section != null && !section.getKeys(false).isEmpty();
    }

    private void deliverPendingForPlayer(Player player) {
        UUID playerId = player.getUniqueId();
        ConfigurationSection section = deliveryLedger.getConfigurationSection("pending-deliveries." + playerId);
        if (section == null) {
            return;
        }

        for (String requestId : new ArrayList<>(section.getKeys(false))) {
            String path = pendingDeliveryPath(playerId, requestId);
            ConfigurationSection receipt = deliveryLedger.getConfigurationSection(path);
            if (receipt == null) {
                continue;
            }
            List<ItemReward> rewards = readPendingRewards(receipt.getMapList("items"));
            if (!deliverRewards(player, rewards, null)) {
                getLogger().warning("Pending coupon reward remains queued for player " + player.getName() + ".");
                continue;
            }

            String redemptionPath = receipt.getString("redemption-path", "");
            if (!finalizeDeliveredReceipt(path, redemptionPath)) {
                getLogger().warning("Delivered coupon receipt could not be finalized; it remains queued and may be delivered again after restart.");
            }
        }
    }

    private List<ItemReward> readPendingRewards(List<Map<?, ?>> entries) {
        List<ItemReward> rewards = new ArrayList<>();
        if (entries == null) {
            return rewards;
        }
        for (Map<?, ?> entry : entries) {
            Object itemId = entry.get("itemId");
            Object amount = entry.get("amount");
            if (!(itemId instanceof String value) || !(amount instanceof Number number)) {
                return List.of();
            }
            rewards.add(new ItemReward(value, number.intValue()));
        }
        return rewards;
    }

    private String pendingDeliveryPath(UUID playerId, String requestId) {
        return "pending-deliveries." + playerId + "." + requestId;
    }

    private boolean finalizeDeliveredReceipt(String path, String redemptionPath) {
        String previousRequestId = "";
        String usernamePath = "";
        String previousPlayerName = "";
        if (!redemptionPath.isBlank()) {
            previousRequestId = getConfig().getString(redemptionPath, "");
            usernamePath = pendingRequestUsernamePath(redemptionPath);
            previousPlayerName = usernamePath.isBlank() ? "" : getConfig().getString(usernamePath, "");
            if (!previousRequestId.isBlank() || !previousPlayerName.isBlank()) {
                getConfig().set(redemptionPath, null);
                if (!usernamePath.isBlank()) {
                    getConfig().set(usernamePath, null);
                }
                if (!persistConfig()) {
                    getConfig().set(redemptionPath, previousRequestId.isBlank() ? null : previousRequestId);
                    if (!usernamePath.isBlank()) {
                        getConfig().set(usernamePath, previousPlayerName.isBlank() ? null : previousPlayerName);
                    }
                    return false;
                }
            }
        }

        ConfigurationSection receipt = deliveryLedger.getConfigurationSection(path);
        Map<String, Object> savedReceipt = receipt == null
                ? null
                : new LinkedHashMap<>(receipt.getValues(false));
        deliveryLedger.set(path, null);
        if (persistDeliveryLedger()) {
            return true;
        }
        if (savedReceipt != null) {
            deliveryLedger.set(path, savedReceipt);
        }
        if (!redemptionPath.isBlank() && (!previousRequestId.isBlank() || !previousPlayerName.isBlank())) {
            getConfig().set(redemptionPath, previousRequestId.isBlank() ? null : previousRequestId);
            if (!usernamePath.isBlank()) {
                getConfig().set(usernamePath, previousPlayerName.isBlank() ? null : previousPlayerName);
            }
            persistConfig();
        }
        return false;
    }

    private boolean persistConfig() {
        Path temporaryFile = null;
        try {
            Files.createDirectories(getDataFolder().toPath());
            temporaryFile = Files.createTempFile(getDataFolder().toPath(), "coupon-config-", ".tmp");
            getConfig().save(temporaryFile.toFile());
            try (var channel = java.nio.channels.FileChannel.open(temporaryFile, StandardOpenOption.WRITE)) {
                channel.force(true);
            }
            Files.move(temporaryFile, new File(getDataFolder(), "config.yml").toPath(),
                    StandardCopyOption.ATOMIC_MOVE, StandardCopyOption.REPLACE_EXISTING);
            return true;
        } catch (IOException | UnsupportedOperationException exception) {
            getLogger().log(Level.SEVERE, "Could not atomically persist coupon state; reward delivery is paused to avoid losing the receipt.", exception);
            return false;
        } finally {
            if (temporaryFile != null) {
                try {
                    Files.deleteIfExists(temporaryFile);
                } catch (IOException exception) {
                    getLogger().log(Level.WARNING, "Could not remove a temporary coupon config file.", exception);
                }
            }
        }
    }

    private boolean persistDeliveryLedger() {
        Path temporaryFile = null;
        try {
            Files.createDirectories(getDataFolder().toPath());
            temporaryFile = Files.createTempFile(getDataFolder().toPath(), "pending-deliveries-", ".tmp");
            deliveryLedger.save(temporaryFile.toFile());
            try (var channel = java.nio.channels.FileChannel.open(temporaryFile, StandardOpenOption.WRITE)) {
                channel.force(true);
            }
            Files.move(temporaryFile, deliveryLedgerFile.toPath(),
                    StandardCopyOption.ATOMIC_MOVE, StandardCopyOption.REPLACE_EXISTING);
            return true;
        } catch (IOException | UnsupportedOperationException exception) {
            getLogger().log(Level.SEVERE, "Could not atomically persist coupon reward ledger; reward delivery is paused.", exception);
            return false;
        } finally {
            if (temporaryFile != null) {
                try {
                    Files.deleteIfExists(temporaryFile);
                } catch (IOException exception) {
                    getLogger().log(Level.WARNING, "Could not remove a temporary coupon ledger file.", exception);
                }
            }
        }
    }

    private ItemStack createItemStack(String itemId, int amount) {
        if (itemId == null || itemId.isBlank() || amount < 1 || amount > maxItemAmount) {
            getLogger().warning("Invalid coupon reward: " + itemId + " x" + amount);
            return null;
        }

        String normalizedId = itemId.trim().toLowerCase(Locale.ROOT);
        Material material = Material.matchMaterial(normalizedId);
        if (material == null && normalizedId.contains(":")) {
            material = Material.matchMaterial(
                    normalizedId.substring(normalizedId.indexOf(':') + 1));
        }
        if (material == null || material == Material.AIR || !material.isItem()) {
            getLogger().warning("Unknown coupon item ID: " + itemId);
            return null;
        }
        return new ItemStack(material, amount);
    }

    private String readBody(InputStream stream) throws IOException {
        byte[] bytes = stream.readNBytes(MAX_RESPONSE_BYTES + 1);
        if (bytes.length > MAX_RESPONSE_BYTES) {
            throw new IOException("Coupon API response exceeded 64KB.");
        }
        return new String(bytes, StandardCharsets.UTF_8);
    }

    private boolean isRetryableStatus(int statusCode) {
        return statusCode == 408 || statusCode == 425 || statusCode == 429 || statusCode >= 500;
    }

    private void validateApiUrl(String value) {
        try {
            URI uri = new URI(value);
            String scheme = uri.getScheme();
            String host = uri.getHost();
            if (host == null || uri.getUserInfo() != null || uri.getQuery() != null || uri.getFragment() != null) {
                throw new IllegalArgumentException("api-url 配置不合法。");
            }
            if (!"http".equalsIgnoreCase(scheme)) {
                throw new IllegalArgumentException("兑换插件 api-url 必须使用内网 HTTP 地址。");
            }

            // 只接受私网 IP 字面地址，避免域名解析变化时把兑换令牌发往公网。
            String addressText = host;
            if (addressText.startsWith("[") && addressText.endsWith("]")) {
                addressText = addressText.substring(1, addressText.length() - 1);
            }
            if (!isIPv4Literal(addressText) && !isIPv6Literal(addressText)) {
                throw new IllegalArgumentException("兑换插件 api-url 必须使用私网 IP 字面地址，不能使用域名。");
            }
            InetAddress address = InetAddress.getByName(addressText);
            if (!isPrivateAddress(address)) {
                throw new IllegalArgumentException("兑换插件 api-url 只能使用 loopback、RFC1918、IPv6 ULA 或 link-local 地址。");
            }
        } catch (URISyntaxException | UnknownHostException exception) {
            throw new IllegalArgumentException("兑换插件 api-url 必须是有效的私网 IP 地址。", exception);
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

    private String trimTrailingQuestionMark(String value) {
        if (value == null || value.isBlank()) {
            return "";
        }
        return value.endsWith("?") ? value.substring(0, value.length() - 1) : value;
    }

    private PendingRequest getOrCreatePendingRequest(UUID playerId, String currentPlayerName, String couponCode) {
        String requestPath = pendingRequestPath(playerId, couponCode);
        String usernamePath = pendingRequestUsernamePath(requestPath);
        String previousRequestId = getConfig().getString(requestPath, "").trim();
        String previousPlayerName = getConfig().getString(usernamePath, "").trim();
        String requestId = previousRequestId.isBlank() ? UUID.randomUUID().toString() : previousRequestId;
        String playerName = previousPlayerName.isBlank() ? currentPlayerName : previousPlayerName;
        getConfig().set(requestPath, requestId);
        getConfig().set(usernamePath, playerName);
        if (!persistConfig()) {
            getConfig().set(requestPath, previousRequestId.isBlank() ? null : previousRequestId);
            getConfig().set(usernamePath, previousPlayerName.isBlank() ? null : previousPlayerName);
            return new PendingRequest("", "");
        }
        return new PendingRequest(requestId, playerName);
    }

    private void clearPendingRequest(UUID playerId, String couponCode) {
        clearPendingRequestPath(pendingRequestPath(playerId, couponCode));
    }

    private void clearPendingRequestPath(String path) {
        getConfig().set(path, null);
        getConfig().set(pendingRequestUsernamePath(path), null);
        persistConfig();
    }

    private String pendingRequestPath(UUID playerId, String couponCode) {
        try {
            byte[] digest = MessageDigest.getInstance("SHA-256")
                    .digest(couponCode.trim().toUpperCase(Locale.ROOT).getBytes(StandardCharsets.UTF_8));
            StringBuilder key = new StringBuilder();
            for (byte value : digest) {
                key.append(String.format(Locale.ROOT, "%02x", value & 0xff));
            }
            return "pending-redemptions." + playerId + "." + key;
        } catch (NoSuchAlgorithmException exception) {
            throw new IllegalStateException("SHA-256 is unavailable.", exception);
        }
    }

    private String pendingRequestUsernamePath(String requestPath) {
        String prefix = "pending-redemptions.";
        if (!requestPath.startsWith(prefix)) {
            return "";
        }
        return "pending-redemption-usernames." + requestPath.substring(prefix.length());
    }

    private boolean isPlaceholderToken(String token) {
        return "DEFAULT_TOKEN".equalsIgnoreCase(token)
                || "CHANGE_ME".equalsIgnoreCase(token);
    }

    @Override
    public void onDisable() {
        pendingRequests.clear();
        getLogger().info("Coupon plugin disabled.");
    }

    private record ItemReward(String itemId, int amount) {
    }

    private record PendingRequest(String requestId, String playerName) {
    }

    private record CouponResponse(boolean success, String message, List<ItemReward> rewards, boolean retryable) {
        private static CouponResponse success(List<ItemReward> rewards) {
            return new CouponResponse(true, "", rewards, false);
        }

        private static CouponResponse failure(String message, boolean retryable) {
            return new CouponResponse(false, message, List.of(), retryable);
        }
    }
}

