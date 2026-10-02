package network

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"containia/pkg/config"
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
	if err := exec.Command("ip", "link", "show", BridgeName).Run(); err != nil {
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
	}

	// 6. Set up iptables NAT Masquerade rule for outbound internet access
	if err := exec.Command("iptables", "-t", "nat", "-C", "POSTROUTING", "-s", BridgeSubnet, "!", "-o", BridgeName, "-j", "MASQUERADE").Run(); err != nil {
		_ = exec.Command("iptables", "-t", "nat", "-A", "POSTROUTING", "-s", BridgeSubnet, "!", "-o", BridgeName, "-j", "MASQUERADE").Run()
	}
	// Masquerade traffic to container subnet (for localhost DNAT hairpin routing)
	if err := exec.Command("iptables", "-t", "nat", "-C", "POSTROUTING", "-d", BridgeSubnet, "-j", "MASQUERADE").Run(); err != nil {
		_ = exec.Command("iptables", "-t", "nat", "-A", "POSTROUTING", "-d", BridgeSubnet, "-j", "MASQUERADE").Run()
	}

	// 7. Ensure FORWARD accept rules for the bridge
	if err := exec.Command("iptables", "-C", "FORWARD", "-i", BridgeName, "-j", "ACCEPT").Run(); err != nil {
		_ = exec.Command("iptables", "-A", "FORWARD", "-i", BridgeName, "-j", "ACCEPT").Run()
	}
	if err := exec.Command("iptables", "-C", "FORWARD", "-o", BridgeName, "-j", "ACCEPT").Run(); err != nil {
		_ = exec.Command("iptables", "-A", "FORWARD", "-o", BridgeName, "-j", "ACCEPT").Run()
	}

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

// SetupPortForwarding configures iptables DNAT rules to publish container ports on host.
func SetupPortForwarding(containerIP string, portMappings []string) error {
	// Allow routing to localhost/DNAT
	_ = os.WriteFile("/proc/sys/net/ipv4/conf/all/route_localnet", []byte("1"), 0644)
	_ = os.WriteFile("/proc/sys/net/ipv4/conf/containia0/route_localnet", []byte("1"), 0644)

	for _, mapping := range portMappings {
		parts := strings.Split(mapping, ":")
		var hostPort, contPort string
		if len(parts) == 2 {
			hostPort = parts[0]
			contPort = parts[1]
		} else if len(parts) == 1 {
			hostPort = parts[0]
			contPort = parts[0]
		} else {
			continue
		}

		targetDest := fmt.Sprintf("%s:%s", containerIP, contPort)

		// 1. Allow forwarded packets in FORWARD chain
		_ = exec.Command("iptables", "-C", "FORWARD", "-p", "tcp", "-d", containerIP, "--dport", contPort, "-j", "ACCEPT").Run()
		_ = exec.Command("iptables", "-A", "FORWARD", "-p", "tcp", "-d", containerIP, "--dport", contPort, "-j", "ACCEPT").Run()

		// 2. External / Routed traffic (PREROUTING)
		_ = exec.Command("iptables", "-t", "nat", "-C", "PREROUTING", "-p", "tcp", "--dport", hostPort, "-j", "DNAT", "--to-destination", targetDest).Run()
		_ = exec.Command("iptables", "-t", "nat", "-A", "PREROUTING", "-p", "tcp", "--dport", hostPort, "-j", "DNAT", "--to-destination", targetDest).Run()

		// 3. Localhost originated traffic (OUTPUT)
		_ = exec.Command("iptables", "-t", "nat", "-C", "OUTPUT", "-p", "tcp", "-o", "lo", "--dport", hostPort, "-j", "DNAT", "--to-destination", targetDest).Run()
		_ = exec.Command("iptables", "-t", "nat", "-A", "OUTPUT", "-p", "tcp", "-o", "lo", "--dport", hostPort, "-j", "DNAT", "--to-destination", targetDest).Run()
	}
	return nil
}

// CleanupPortForwarding deletes iptables rules for a container.
func CleanupPortForwarding(containerIP string, portMappings []string) {
	for _, mapping := range portMappings {
		parts := strings.Split(mapping, ":")
		var hostPort, contPort string
		if len(parts) == 2 {
			hostPort = parts[0]
			contPort = parts[1]
		} else if len(parts) == 1 {
			hostPort = parts[0]
			contPort = parts[0]
		} else {
			continue
		}

		targetDest := fmt.Sprintf("%s:%s", containerIP, contPort)
		_ = exec.Command("iptables", "-D", "FORWARD", "-p", "tcp", "-d", containerIP, "--dport", contPort, "-j", "ACCEPT").Run()
		_ = exec.Command("iptables", "-t", "nat", "-D", "PREROUTING", "-p", "tcp", "--dport", hostPort, "-j", "DNAT", "--to-destination", targetDest).Run()
		_ = exec.Command("iptables", "-t", "nat", "-D", "OUTPUT", "-p", "tcp", "-o", "lo", "--dport", hostPort, "-j", "DNAT", "--to-destination", targetDest).Run()
	}
}

// SyncAllContainerHosts synchronizes /etc/hosts for all running containers so they can resolve each other by name.
func SyncAllContainerHosts() error {
	containersDir := config.GetContainersDir()
	entries, err := os.ReadDir(containersDir)
	if err != nil {
		return err
	}

	// 1. Collect all active containers with IP addresses
	var activeHosts []string
	activeHosts = append(activeHosts,
		"127.0.0.1   localhost",
		"::1         localhost ip6-localhost ip6-loopback",
		fmt.Sprintf("%s containia-bridge host.containia.internal", BridgeGateway),
	)

	type ContInfo struct {
		ID   string
		Name string
		IP   string
	}
	var activeContainers []ContInfo

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		cid := entry.Name()
		statePath := config.GetContainerStatePath(cid)
		data, err := os.ReadFile(statePath)
		if err != nil {
			continue
		}
		var st config.ContainerState
		if err := json.Unmarshal(data, &st); err != nil {
			continue
		}
		if st.IPAddress != "" && (st.Status == config.StatusRunning || st.Status == config.StatusCreated) {
			activeContainers = append(activeContainers, ContInfo{
				ID:   cid,
				Name: st.Name,
				IP:   st.IPAddress,
			})
			activeHosts = append(activeHosts, fmt.Sprintf("%s %s %s", st.IPAddress, st.Name, cid[:min(len(cid), 12)]))
		}
	}

	hostsContent := strings.Join(activeHosts, "\n") + "\n"

	// 2. Write /etc/hosts in every container's merged directory
	for _, c := range activeContainers {
		mergedDir := config.GetContainerMergedDir(c.ID)
		etcDir := filepath.Join(mergedDir, "etc")
		if _, err := os.Stat(etcDir); err == nil {
			hostsPath := filepath.Join(etcDir, "hosts")
			_ = os.WriteFile(hostsPath, []byte(hostsContent), 0644)
		}
	}

	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
