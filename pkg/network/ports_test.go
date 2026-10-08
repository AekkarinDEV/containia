package network

import (
	"reflect"
	"testing"
)

func TestDefaultPortMappings(t *testing.T) {
	ports := map[string]interface{}{
		"8000/tcp": nil, "5432/tcp": nil, "3000/tcp": nil,
		"3000": nil, "53/udp": nil, "0/tcp": nil, "65536/tcp": nil, "bad": nil,
	}
	want := []string{"3000:3000", "5432:5432", "8000:8000"}
	if got := DefaultPortMappings(ports); !reflect.DeepEqual(got, want) {
		t.Fatalf("default ports = %v, want %v", got, want)
	}
}
