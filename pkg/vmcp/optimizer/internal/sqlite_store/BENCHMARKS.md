# SQLite ToolStore Benchmarks

<!-- TODO: These benchmarks are a quality/performance practice rather than
     functional tests of sqlite_store. Consider moving them to a dedicated
     benchmarking repo or similar in the future. -->

Benchmarks measure search performance over 1,000 tools with synthetic descriptions.

Run benchmarks with:

```bash
go test -bench=. -benchmem -count=3 -timeout=10m \
  ./pkg/vmcp/optimizer/internal/sqlite_store/ -run='^$'
```

## Baseline Results (PR #3808)

Environment: Apple M3 Pro, darwin/arm64, Go 1.25.7

| Benchmark | ns/op (avg) | B/op | allocs/op |
|-----------|-------------|------|-----------|
| FTS5Only_1000Tools | 234,025,572 | ~37,000 | ~123 |
| Semantic_1000Tools_384Dim | 2,528,449 | 5,052,507 | 14,054 |
| Hybrid_1000Tools | 233,994,955 | 5,115,859 | 14,186 |
| Semantic_1000Tools_768Dim | 3,776,112 | 9,662,143 | 14,055 |

### Key Observations

- **Semantic search is ~92x faster than FTS5** for latency (~2.5ms vs ~234ms).
- **FTS5 dominates hybrid search time**: Hybrid takes ~234ms, nearly identical to
  FTS5-only, meaning the semantic portion adds negligible latency overhead.
- **FTS5 is memory-efficient**: ~37KB per search vs ~5MB (384-dim) or ~9.6MB (768-dim)
  for semantic search.
- **Doubling embedding dimensions** (384 to 768) increases semantic search latency by
  ~49% and memory by ~91%, with allocation count unchanged.
- **Hybrid search** combines FTS5 latency with semantic memory cost — optimization
  efforts should focus on reducing FTS5 search time.
