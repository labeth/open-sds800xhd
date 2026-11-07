package main

import "testing"

func TestFaultCounts(t *testing.T) {
	m := faultCounts("on [dma timeout left=-1] fired [bits=3 dma timeout=4 irq dmadata_irq=2 key chatter=15]")
	if m["dma timeout"] != 4 || m["irq dmadata_irq"] != 2 || m["bits"] != 3 || m["key chatter"] != 15 {
		t.Fatalf("%v", m)
	}
}
