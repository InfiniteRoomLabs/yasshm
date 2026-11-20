// Package deprule holds no code. Its tests enforce rules about the
// repository's structure by reading its files: the dependency rule of spec
// section 4 (the imports of every Go file under internal/), what the
// end-to-end suite (test/e2e) may import, and the section 6 tables that
// name, for each security guarantee, the tests that prove it (every named
// test must exist).
package deprule
