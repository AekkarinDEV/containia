package network

import (
	"sort"
	"strconv"
	"strings"
)

// DefaultPortMappings publishes each TCP port declared by the image at the same host port.
func DefaultPortMappings(exposed map[string]interface{}) []string {
	ports := make(map[int]bool)
	for entry := range exposed {
		portText, protocol, hasProtocol := strings.Cut(entry, "/")
		if hasProtocol && protocol != "tcp" {
			continue
		}
		port, err := strconv.Atoi(portText)
		if err == nil && port > 0 && port <= 65535 {
			ports[port] = true
		}
	}
	var ordered []int
	for port := range ports {
		ordered = append(ordered, port)
	}
	sort.Ints(ordered)
	result := make([]string, 0, len(ordered))
	for _, port := range ordered {
		value := strconv.Itoa(port)
		result = append(result, value+":"+value)
	}
	return result
}
