package strip

import "testing"

func literalIDBag(t *testing.T, abs string, src []byte) map[string]int {
	t.Helper()
	return literalIDs(abs, src)
}

func commentNodeCounts(t *testing.T, abs string, src []byte) (int, int) {
	t.Helper()
	n, g, err := commentCounts(abs, src)
	if err != nil {
		t.Fatalf("commentCounts(%s): %v", abs, err)
	}
	return n, g
}
