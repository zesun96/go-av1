package preset

import "testing"

func TestResolveSpeedOrdering(t *testing.T) {
	slow := Resolve(0)
	p12 := Resolve(12)
	fast := Resolve(13)
	if slow.SearchRange < p12.SearchRange || p12.SearchRange < fast.SearchRange {
		t.Fatalf("search ranges slow=%d p12=%d fast=%d",
			slow.SearchRange, p12.SearchRange, fast.SearchRange)
	}
	if !p12.HierarchicalME || !p12.Compound {
		t.Fatalf("preset 12=%+v", p12)
	}
	if fast.Compound || !fast.IntegerOnly {
		t.Fatalf("preset 13=%+v", fast)
	}
}
