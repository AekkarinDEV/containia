package network

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
)

const (
	BridgeName    = "containia0"
	BridgeSubnet  = "172.19.0.0/24"
	BridgeGateway = "172.19.0.1"
)

// SetupBridge ensures the Linux bridge interface 'containia0' exists and has IP forwarding enabled.
func SetupBridge() error {
	// 1. Enable IPv4 forwarding on host
	_ = os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0644)

	// 2. Check if bridge already exists
	if err := exec.Command("ip", "link", "show", BridgeName).Run(); err == nil {
		return nil
	}

	// 3. Create bridge
	if out, err := exec.Command("ip", "link", "add", "name", BridgeName, "type", "bridge").CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create bridge %s: %s (%w)", BridgeName, string(out), err)
	}

	// 4. Assign IP to bridge
	if out, err := exec.Command("ip", "addr", "add", BridgeGateway+"/24", "dev", BridgeName).CombinedOutput(); err != nil {
		return fmt.Errorf("failed to assign IP to bridge: %s (%w)", string(out), err)
	}

	// 5. Bring bridge interface UP
	if out, err := exec.Command("ip", "link", "set", BridgeName, "up").CombinedOutput(); err != nil {
		return fmt.Errorf("failed to bring bridge up: %s (%w)", string(out), err)
	}

	// 6. Set up iptables NAT Masquerade rule for outbound internet access
	_ = exec.Command("iptables", "-t", "nat", "-C", "POSTROUTING", "-s", BridgeSubnet, "!", "-o", BridgeName, "-j", "MASQUERADE").Run()
	_ = exec.Command("iptables", "-t", "nat", "-A", "POSTROUTING", "-s", BridgeSubnet, "!", "-o", BridgeName, "-j", "MASQUERADE").Run()

	return nil
}

// SetupContainerNetwork creates a veth pair, connects one end to the bridge, and moves the other end into the container network namespace.
func SetupContainerNetwork(containerID string, pid int) (string, error) {
	if err := SetupBridge(); err != nil {
		return "", err
	}

	// Short names for veth interfaces (max Linux ifname is 15 chars)
	shortID := containerID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	hostVeth := "vh-" + shortID
	contVeth := "vc-" + shortID

	// Clean up stale veth if exists
	_ = exec.Command("ip", "link", "delete", hostVeth).Run()

	// 1. Create veth pair
	if out, err := exec.Command("ip", "link", "add", hostVeth, "type", "veth", "peer", "name", contVeth).CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to create veth pair: %s (%w)", string(out), err)
	}

	// 2. Attach host end to containia0 bridge and set UP
	if out, err := exec.Command("ip", "link", "set", hostVeth, "master", BridgeName).CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to attach veth to bridge: %s (%w)", string(out), err)
	}
	_ = exec.Command("ip", "link", "set", hostVeth, "up").Run()

	// 3. Move container end into the container process network namespace
	pidStr := fmt.Sprintf("%d", pid)
	if out, err := exec.Command("ip", "link", "set", contVeth, "netns", pidStr).CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to move veth to container netns: %s (%w)", string(out), err)
	}

	// 4. Generate deterministic IP address for container (172.19.0.2 - 172.19.0.254)
	ipAddr := generateContainerIP(containerID)

	// 5. Configure network inside container namespace using 'nsenter' or 'ip netns'
	// Rename to eth0, assign IP, set up, and add default gateway
	commands := [][]string{
		{"nsenter", "-t", pidStr, "-n", "ip", "link", "set", contVeth, "name", "eth0"},
		{"nsenter", "-t", pidStr, "-n", "ip", "addr", "add", ipAddr + "/24", "dev", "eth0"},
		{"nsenter", "-t", pidStr, "-n", "ip", "link", "set", "lo", "up"},
		{"nsenter", "-t", pidStr, "-n", "ip", "link", "set", "eth0", "up"},
		{"nsenter", "-t", pidStr, "-n", "ip", "route", "add", "default", "via", BridgeGateway, "dev", "eth0"},
	}

	for _, cmdArgs := range commands {
		_ = exec.Command(cmdArgs[0], cmdArgs[1:]...).Run()
	}

	return ipAddr, nil
}

// CleanupContainerNetwork removes the host-side veth interface.
func CleanupContainerNetwork(containerID string) {
	shortID := containerID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	hostVeth := "vh-" + shortID
	_ = exec.Command("ip", "link", "delete", hostVeth).Run()
}

// Generate container IP from container ID hash
func generateContainerIP(containerID string) string {
	hash := sha256.Sum256([]byte(containerID))
	// range 2 to 254
	lastOctet := 2 + (int(hash[0]) % 250)
	return fmt.Sprintf("172.19.0.%d", lastOctet)
}
