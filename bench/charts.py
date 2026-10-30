"""Charts of a bench run for the README and BENCHMARK.md: python3 charts.py <results/run> <out dir>.

Writes <name>-light.png and <name>-dark.png for each chart, so a <picture> element can follow the GitHub theme.
Needs matplotlib.
"""

import json
import sys
from pathlib import Path

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt  # noqa: E402

DBA, CRYSTAL = "#2A9D8F", "#8C959F"
THEMES = {
    "light": {"bg": "#ffffff", "fg": "#1F2328", "grid": "#d0d7de", "bad": "#CF222E"},
    "dark": {"bg": "#0d1117", "fg": "#E6EDF3", "grid": "#30363d", "bad": "#F85149"},
}


def style(ax, t):
    ax.set_facecolor(t["bg"])
    ax.tick_params(which="both", colors=t["fg"], labelsize=9)
    for s in ax.spines.values():
        s.set_visible(False)
    ax.grid(axis="x", color=t["grid"], linewidth=0.6)
    ax.set_axisbelow(True)
    ax.xaxis.label.set_color(t["fg"])
    ax.title.set_color(t["fg"])


def legend(ax, t, **kw):
    lg = ax.legend(frameon=False, fontsize=9, **kw)
    for txt in lg.get_texts():
        txt.set_color(t["fg"])


def save(fig, out, name, theme):
    fig.savefig(out / f"{name}-{theme}.png", dpi=200, bbox_inches="tight", facecolor=fig.get_facecolor())
    plt.close(fig)


def tokens(run, out):
    rows = json.loads((run / "n2.json").read_text())["rows"]
    labels = [f'{r["id"]} {r["tool"]}' for r in rows][::-1]
    cells = [r["cells"] for r in rows][::-1]
    for theme, t in THEMES.items():
        fig, ax = plt.subplots(figsize=(8, 7.5), facecolor=t["bg"])
        style(ax, t)
        y = range(len(rows))
        for off, srv, color, name in ((0.2, "dba", DBA, "postgres-dba-mcp"), (-0.2, "crystal", CRYSTAL, "crystaldba 0.3.0")):
            vals = [c[srv]["tokens"] if c[srv]["status"] == "ok" else 0 for c in cells]
            ax.barh([i + off for i in y], vals, height=0.38, color=color, label=name)
            for i, c in enumerate(cells):
                if c[srv]["status"] != "ok":
                    ax.text(12, i + off, c[srv]["status"], va="center", fontsize=7.5, color=t["fg"])
        ax.set_xscale("log")
        ax.set_xlim(10, 1e6)
        ax.set_yticks(list(y), labels, fontsize=8.5)
        ax.set_xlabel("tokens in the tool response (log scale, claude -p count)")
        legend(ax, t, loc="lower right")
        save(fig, out, "tokens", theme)


def ops(run, out):
    o = json.loads((run / "ops.json").read_text())
    panels = [
        ("memory after 100 calls (MB, median of 3)", o["rss_mb"], False),
        ("cold start to initialize (ms, median of 20)", o["cold_start_ms"], True),
    ]
    for theme, t in THEMES.items():
        fig, axes = plt.subplots(1, 2, figsize=(8, 2.2), facecolor=t["bg"])
        for ax, (title, m, log) in zip(axes, panels):
            style(ax, t)
            vals = [m["dba"]["value"], m["crystal"]["value"]]
            ax.barh([1, 0], vals, color=[DBA, CRYSTAL], height=0.6)
            ax.set_yticks([1, 0], ["postgres-dba-mcp", "crystaldba 0.3.0"], fontsize=9)
            for i, v in zip([1, 0], vals):
                ax.text(v * (1.15 if log else 1.02), i, f"{v:.1f}" if v < 100 else f"{v:.0f}", va="center", fontsize=9, color=t["fg"])
            if log:
                ax.set_xscale("log")
                ax.set_xlim(1, 2e4)
            else:
                ax.set_xlim(0, max(vals) * 1.25)
            ax.set_title(title, fontsize=9.5, loc="left", color=t["fg"])
        fig.tight_layout()
        save(fig, out, "ops", theme)


def security(run, out):
    s = json.loads((run / "security.json").read_text())["attacks"]
    # Refused in the server's color; failed at execution hatched; executed in red, since it got past the check.
    for theme, t in THEMES.items():
        fig, ax = plt.subplots(figsize=(8, 1.8), facecolor=t["bg"])
        style(ax, t)
        for row, (srv, color) in enumerate((("crystal", CRYSTAL), ("dba", DBA))):
            cases, left, parts = s[srv], 0, []
            for cls, kw in (("refused", {"color": color}),
                            ("failed at execution", {"color": t["bg"], "edgecolor": t["fg"], "hatch": "////", "linewidth": 0}),
                            ("executed", {"color": t["bad"]})):
                n = sum(1 for c in cases if c["class"] == cls)
                if n:
                    ax.barh(row, n, left=left, height=0.6, **kw)
                    left += n
                    parts.append(f"{n} {cls}")
            effects = sum(1 for c in cases if c.get("side_effect"))
            parts.append(f"{effects} with a side effect")
            ax.text(len(cases) + 3, row, ", ".join(parts), va="center", fontsize=8.5, color=t["fg"], clip_on=False)
        ax.set_yticks([0, 1], ["crystaldba 0.3.0", "postgres-dba-mcp"], fontsize=9)
        ax.set_xlim(0, 124)
        ax.set_xlabel("124 attack statements in restricted mode")
        save(fig, out, "security", theme)


if __name__ == "__main__":
    run, out = Path(sys.argv[1]), Path(sys.argv[2])
    out.mkdir(parents=True, exist_ok=True)
    tokens(run, out)
    ops(run, out)
    security(run, out)
