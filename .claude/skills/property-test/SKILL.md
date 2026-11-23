---
name: property-test
description: This skill should be used when the user wants to "write a property test", "add PBT", "test this with rapid", "check this invariant on random inputs", "run the mutation check", or pick up the next candidate in doc/FORMAL_FIT.md for the klein-cli project.
---

Write property-based tests (PBT) for klein's pure logic with
[`pgregory.net/rapid`](https://github.com/flyingmutant/rapid), then prove the
tests can catch bugs by planting mutations. No Lean or model checking: klein's
targets are small enough that a property against a reference model gets nearly
all of the value.

## Where it fits

Use PBT on code that is **pure and has a statable invariant** — parsers, state
machines over a byte stream, split/truncate/range logic. `doc/FORMAL_FIT.md` is
the ranked list of candidates with their evidence; start there and update it as
targets are done.

Do not force PBT onto:

- **Concurrency / reconnect protocols** (`pkg/agentserver` runner, gateway
  scheduler reconcile, background agent runs). Function-level tests do not find
  design errors there. Record them in `FORMAL_FIT.md` as model-checking (TLA+)
  candidates and move on.
- **Thin wrappers over a library** (e.g. `nextCronFire` over `robfig/cron`).
- **Code where most of the work is I/O** and no pure core can be cut out —
  propose the extraction first.

To find new candidates, look for fix commits by path. Many klein fixes do not
start with `fix`, so read subjects rather than trusting a count:

```bash
git log --format='%h %ad %s' --date=short -- <path>
```

## Steps

1. **State the properties first.** Write each property as one sentence and
   number it (S1, S2, …) per target. The same number goes in the test's doc
   comment and in `FORMAL_FIT.md`. If you cannot write a property, the spec is
   unclear — go to step 6 before writing code.

2. **Write a generator** in `pkg/testhelper/<name>gen` (a normal package, so
   several test packages can share it; see `pkg/testhelper/messagegen`).
   - Shape inputs the way production produces them, and cite the producer in
     the doc comment (e.g. "the way ReAct writes it (pkg/agent/react/react.go)").
   - Raise the odds of boundaries on purpose — uniform random almost never hits
     them: empty input, a marker split across chunks, a canceled batch, a
     header that miscounts its body, a budget smaller than the marker.
   - List the boundaries the generator targets in its doc comment.

3. **Write the properties** as `Test<Func>_<Property>` with
   `t.Parallel()` and `rapid.Check`.
   - **No self-reference**: never compute the expected value with the function
     under test. Write a small reference model that states the rule directly
     (e.g. pair-by-pair, brute force), even if it is O(n²).
   - Inputs outside a precondition: `t.Skip`. If rapid reports
     "only generated N valid tests", fix the generator instead. A property that
     is vacuously true for an input (e.g. "no split happened") should `return`,
     not skip.
   - Behavior nobody has decided on is **excluded** from the property and pinned
     in a separate test (step 6).

4. **Run it.** The default 100 checks first, then many more:

   ```bash
   go test ./pkg/<pkg>/ -count=1 -run '<TestRegex>'
   go test ./pkg/<pkg>/ -count=1 -run '<TestRegex>' -rapid.checks=20000
   ```

   `-rapid.checks` is only defined in packages that import rapid — never pass
   it to `go test ./...`. On failure rapid prints a minimized counterexample
   and a `-rapid.failfile=` to reproduce it. `testdata/rapid/` is gitignored;
   delete it before committing anyway.

   If the property fails on current code, that is a finding: show the minimal
   counterexample to the user before changing production code.

5. **Test the tests with mutations.** Plant bugs and confirm some property
   catches each one. Keep the mutations next to this skill as
   `mutations/<target>.tsv` (one per line: `name<TAB>file<TAB>perl s///`,
   applied with `perl -0pi`, so `\n` can span lines):

   ```bash
   .claude/skills/property-test/scripts/mutate.sh \
     .claude/skills/property-test/mutations/compaction.tsv \
     "S1=go test ./pkg/agent/state/ -count=1 -run '^TestIsSafeSplitPoint_MatchesPairs$' -rapid.checks=1000" \
     "S2=go test ./pkg/agent/state/ -count=1 -run '^TestFindSafeSplitPoint_LatestSafe$' -rapid.checks=1000"
   ```

   Pick mutations from: the reverse of a past fix commit (most convincing),
   off-by-one (`<`↔`<=`, `s-1`), dropping one side of a condition, widening a
   bound, and disabling the branch the generator's boundary is meant to reach
   (this is how you confirm the boundary is actually generated). Every
   mutation should be caught by at least one property; explain any miss in the
   PR (e.g. a safety-only property cannot see a mutation that is merely too
   cautious).

6. **Pin undecided behavior; don't fix it.** Writing a property often forces a
   question the code never answered (what happens when the hunk header
   overcounts? should a lone Esc wait for the next key?). Then:
   - Look for an answer first: `doc/`, `CLAUDE.md`, comments, the commit that
     introduced the behavior. If it is decided, cite it and write the property
     with that exception.
   - Otherwise write `Test<Func>_<CurrentBehavior>` that pins what the code does
     today, with a doc comment saying it is not confirmed as intended.
   - Add it under "Team decision" for that target in `FORMAL_FIT.md`, and ask
     the user — don't change production behavior on your own.
   - Say whether it can happen in practice (trace the callers); if you could
     not check, say "unverified" rather than guessing.

7. **Verify like CI.** `go vet` and `golangci-lint run` on the touched
   packages. Imports follow the repo's gci order — standard, third party
   (`pgregory.net/rapid`), then `github.com/fpt/...` — each group separated by
   a blank line.

## Done when

- [ ] Each property is numbered and stated in one line in its doc comment.
- [ ] The generator targets named boundaries, and a mutation confirmed the
      boundary branch is reached.
- [ ] Mutation results are in a table; misses are explained.
- [ ] Undecided behavior is pinned in its own test and listed in
      `FORMAL_FIT.md`, not silently fixed.
- [ ] Passes at `-rapid.checks=20000`; lint and existing tests unaffected.
