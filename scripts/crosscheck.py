#!/usr/bin/env python3
"""Cross-check tcount against ccusage on the same logs.

    scripts/crosscheck.py                 # the real logs under $HOME
    scripts/crosscheck.py --home DIR      # a fixture tree laid out like a home
                                          # (DIR/.claude, DIR/.codex, DIR/.grok, DIR/.gemini)

Both tools are pointed at the same directories through their environment
variables, every field is compared per agent, and the exit status is 1 if any
field differs for a reason not listed in KNOWN below.
"""
import argparse, json, os, subprocess, sys

AGENTS = ["claude", "codex", "grok", "gemini"]
# tcount snapshot.all field -> ccusage totals field
FIELDS = [("input", "inputTokens"), ("cache_read", "cacheReadTokens"),
          ("cache_write", "cacheCreationTokens"), ("output", "outputTokens"),
          ("total", "totalTokens")]

# Documented differences in how the two tools *report* the same usage. Each
# maps (agent, field) to (reason, f(tcount) -> the value ccusage should show).
# A diff is accepted only if ccusage shows exactly that value.
KNOWN = {
    ("gemini", "output"): (
        "ccusage keeps Gemini thoughts out of outputTokens (adds them to the "
        "total only); tcount folds reasoning into output",
        lambda t: t["output"] - t["reasoning"]),
}

def env_for(home):
    env = dict(os.environ)
    if home:
        home = os.path.abspath(home)
        env.update(CLAUDE_CONFIG_DIR=f"{home}/.claude", CODEX_HOME=f"{home}/.codex",
                   GROK_HOME=f"{home}/.grok", GEMINI_DIR=f"{home}/.gemini",
                   GEMINI_DATA_DIR=f"{home}/.gemini/tmp")
    return env

def tcount(agent, env, repo):
    out = subprocess.run(["go", "run", ".", "all", "--json", "--no-cache", "-a", agent],
                         cwd=repo, env=env, capture_output=True, text=True, check=True).stdout
    return json.loads(out)["snapshot"]["all"]

def ccusage(agent, env):
    p = subprocess.run(["npx", "-y", "ccusage@latest", agent, "daily", "--json", "--offline"],
                       env=env, capture_output=True, text=True)
    try:
        return json.loads(p.stdout).get("totals") or {}
    except json.JSONDecodeError:
        return {}  # no data for this agent

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--home", help="fixture dir laid out like $HOME")
    ap.add_argument("--agents", default=",".join(AGENTS))
    a = ap.parse_args()
    repo = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    env = env_for(a.home)
    bad = 0
    print(f"{'agent':7} {'field':12} {'tcount':>14} {'ccusage':>14} {'diff':>12}  status")
    for agent in a.agents.split(","):
        t, c = tcount(agent, env, repo), ccusage(agent, env)
        for tf, cf in FIELDS:
            tv, cv = t.get(tf, 0), int(c.get(cf, 0) or 0)
            status = "ok"
            if tv != cv:
                known = KNOWN.get((agent, tf))
                if known and known[1](t) == cv:
                    status = "known: " + known[0]
                else:
                    status, bad = "MISMATCH", bad + 1
            print(f"{agent:7} {tf:12} {tv:>14,} {cv:>14,} {tv - cv:>+12,}  {status}")
    print("FAIL: %d unexplained mismatch(es)" % bad if bad else "PASS")
    return 1 if bad else 0

if __name__ == "__main__":
    sys.exit(main())
