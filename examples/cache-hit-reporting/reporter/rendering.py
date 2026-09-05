"""Canonical HTML and plain-text report artifacts, shared by all integrations."""

import html
from typing import Any


class ReportRenderer:
    version = "cache-report-v2"

    @staticmethod
    def _count(value: float) -> str:
        rounded = round(value)
        if abs(value - rounded) < 0.01:
            return f"{rounded:,}"
        return f"{value:,.2f}"

    def render_text(self, report: dict[str, Any], detail_url: str = "") -> str:
        overall = report["overall"]
        lines = [
            "Bifrost daily cache report",
            f"Severity: {report['assessment']['level'].upper()}",
            report['assessment']['reason'],
            f"Window: {report['period_start']} to {report['period_end']}",
            "",
            f"Overall cache hit rate: {overall['hit_rate_percent']:.2f}%",
            f"Requests: {self._count(overall['requests'])}",
            f"Hits: {self._count(overall['hits'])}",
            f"Misses: {self._count(overall['misses'])}",
        ]
        for cache_type, value in sorted(overall["hits_by_type"].items()):
            lines.append(f"{cache_type.title()} hits: {self._count(value)}")

        if report["details"]:
            lines.extend(["", f"Details by {', '.join(report['group_by'])}:"])
            for row in report["details"]:
                identity = ", ".join(f"{key}={value or '(empty)'}" for key, value in row["labels"].items())
                types = ", ".join(f"{key}={self._count(value)}" for key, value in sorted(row["hits_by_type"].items())) or "none"
                lines.append(
                    f"- {identity}: {row['hit_rate_percent']:.2f}% "
                    f"({self._count(row['hits'])}/{self._count(row['requests'])}), hit types: {types}"
                )
        if detail_url:
            lines.extend(["", f"Open full report: {detail_url}"])
        return "\n".join(lines) + "\n"

    def render_html(self, report: dict[str, Any], detail_url: str = "") -> str:
        overall = report["overall"]
        hit_rate = overall["hit_rate_percent"]
        assessment = report['assessment']
        rate_color, rate_background = {
            "normal": ("#067647", "#ecfdf3"),
            "warning": ("#93370d", "#fffaeb"),
            "critical": ("#b42318", "#fef3f2"),
            "unknown": ("#475467", "#f2f4f7"),
        }[assessment['level']]
        severity_label = html.escape(assessment['level'].title())
        reason = html.escape(assessment['reason'])
        hit_rate_display = f"{hit_rate:.2f}%" if overall['requests'] else "N/A"
        detail_link = (
            f'<p style="margin-top:10px"><a href="{html.escape(detail_url, quote=True)}" '
            'style="color:#344054">Open full report</a></p>' if detail_url else ""
        )

        type_rows = []
        for cache_type, value in sorted(overall["hits_by_type"].items()):
            share = 100.0 * value / overall["hits"] if overall["hits"] else 0.0
            type_rows.append(
                '<tr class="data-row">'
                '<td class="type-name">'
                '<span class="type-dot">&bull;</span>'
                f"{html.escape(cache_type.title())}"
                "</td>"
                f'<td class="number">{self._count(value)}</td>'
                f'<td class="number muted">{share:.1f}%</td>'
                "</tr>"
            )
        if not type_rows:
            type_rows.append(
                '<tr class="data-row"><td class="muted" colspan="3">'
                "No cache hits were recorded in this window."
                "</td></tr>"
            )

        rows = []
        for row in report["details"]:
            identity = "".join(
                '<div class="identity">'
                f'<span class="identity-key">{html.escape(key)}</span>'
                f'<span class="identity-value">{html.escape(value or "(empty)")}</span>'
                "</div>"
                for key, value in row["labels"].items()
            )
            cache_types = "<br>".join(
                f'<span class="type-detail">{html.escape(key.title())} {self._count(value)}</span>'
                for key, value in sorted(row["hits_by_type"].items())
            ) or '<span class="muted">None</span>'
            misses = max(0.0, row["requests"] - row["hits"])
            rows.append(
                '<tr class="data-row">'
                f'<td class="identity-cell">{identity}</td>'
                f'<td class="number">{self._count(row["requests"])}</td>'
                f'<td class="number">{self._count(row["hits"])}</td>'
                f'<td class="number muted">{self._count(misses)}</td>'
                f'<td class="number rate-cell">{row["hit_rate_percent"]:.2f}%</td>'
                f'<td class="breakdown">{cache_types}</td>'
                "</tr>"
            )
        details_table = ""
        if rows:
            details_table = (
                '<div class="section-heading">'
                f"<h2>Performance by {html.escape(', '.join(report['group_by']))}</h2>"
                f'<span class="section-note">Top {len(rows)} by request volume</span>'
                "</div>"
                '<div class="table-wrap"><table class="detail-table" role="table">'
                "<thead><tr><th>Identity</th><th>Requests</th><th>Hits</th>"
                "<th>Misses</th><th>Hit rate</th><th>Hit types</th></tr></thead><tbody>"
                + "".join(rows)
                + "</tbody></table></div>"
            )
        else:
            details_table = (
                '<div class="section-heading">'
                f"<h2>Performance by {html.escape(', '.join(report['group_by']))}</h2>"
                "</div>"
                '<div class="empty-state">No grouped request activity was recorded in this window.</div>'
            )

        period_start = html.escape(report["period_start"])
        period_end = html.escape(report["period_end"])
        window = html.escape(report["window"])
        return f"""<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Bifrost daily cache report</title>
<style>
body{{margin:0;background:#f4f6f8;color:#182230;font-family:Inter,-apple-system,BlinkMacSystemFont,"Segoe UI",Arial,sans-serif}}
table{{border-collapse:collapse;border-spacing:0}} h1,h2,p{{margin:0}}
.shell{{width:100%;background:#f4f6f8;padding:32px 12px}}
.container{{width:680px;max-width:680px;background:#ffffff;border:1px solid #e4e7ec;border-radius:10px;overflow:hidden}}
.header{{padding:30px 36px 24px;border-bottom:1px solid #eaecf0}}
.brand{{color:#344054;font-size:12px;font-weight:700;letter-spacing:1.5px;text-transform:uppercase}}
h1{{margin-top:8px;font-size:25px;line-height:1.25;font-weight:650;color:#101828}}
.period{{margin-top:9px;color:#667085;font-size:13px;line-height:1.55}}
.content{{padding:28px 36px 32px}}
.summary{{background:#f8fafc;border:1px solid #e4e7ec;border-radius:8px;padding:22px 24px}}
.summary-label{{font-size:12px;font-weight:650;letter-spacing:.5px;text-transform:uppercase;color:#667085}}
.summary-rate{{margin-top:5px;font-size:38px;line-height:1;font-weight:700;letter-spacing:-1px;color:{rate_color}}}
.summary-context{{margin-top:8px;color:#475467;font-size:13px}}
.rate-status{{display:inline-block;margin-left:7px;padding:3px 8px;border-radius:999px;background:{rate_background};color:{rate_color};font-size:11px;font-weight:650;vertical-align:2px}}
.kpis{{width:100%;margin-top:16px}}
.kpi{{width:33.333%;padding:15px 16px;border:1px solid #e4e7ec;background:#fff}}
.kpi:first-child{{border-radius:7px 0 0 7px}} .kpi:last-child{{border-radius:0 7px 7px 0}}
.kpi-label{{font-size:11px;letter-spacing:.45px;text-transform:uppercase;color:#667085;font-weight:650}}
.kpi-value{{display:block;margin-top:5px;color:#101828;font-size:21px;font-weight:650}}
.section{{margin-top:30px}}
.section-heading{{margin-bottom:12px}}
.section-heading h2{{display:inline;color:#101828;font-size:16px;font-weight:650}}
.section-note{{float:right;color:#98a2b3;font-size:12px;line-height:20px}}
.compact-table,.detail-table{{width:100%;border:1px solid #e4e7ec}}
th{{padding:10px 12px;background:#f8fafc;border-bottom:1px solid #e4e7ec;color:#475467;font-size:11px;font-weight:650;letter-spacing:.25px;text-align:left;text-transform:uppercase}}
td{{padding:11px 12px;border-bottom:1px solid #eaecf0;color:#344054;font-size:13px;line-height:1.4}}
.data-row:last-child td{{border-bottom:0}}
.number{{text-align:right;font-variant-numeric:tabular-nums;white-space:nowrap}}
.muted{{color:#667085}} .type-name{{font-weight:600;color:#344054}}
.type-dot{{color:#6172f3;font-size:18px;line-height:10px;margin-right:7px;vertical-align:-1px}}
.identity{{margin:1px 0 5px}} .identity:last-child{{margin-bottom:1px}}
.identity-key{{display:block;color:#667085;font-size:10px;font-weight:650;letter-spacing:.35px;text-transform:uppercase}}
.identity-value{{display:block;margin-top:1px;color:#182230;font-weight:600;word-break:break-word}}
.identity-cell{{min-width:128px}} .rate-cell{{font-weight:650;color:#182230}}
.breakdown{{white-space:nowrap}} .type-detail{{color:#475467;font-size:12px}}
.empty-state{{padding:18px;border:1px solid #e4e7ec;border-radius:7px;background:#f8fafc;color:#667085;font-size:13px}}
.table-wrap{{overflow-x:auto;border-radius:7px}}
.footer{{padding:20px 36px;background:#f8fafc;border-top:1px solid #eaecf0;color:#667085;font-size:11px;line-height:1.6}}
.preheader{{display:none!important;visibility:hidden;opacity:0;color:transparent;height:0;width:0;overflow:hidden}}
@media only screen and (max-width:620px){{
  .shell{{padding:0}} .container{{width:100%!important;border-radius:0;border-left:0;border-right:0}}
  .header,.content,.footer{{padding-left:20px!important;padding-right:20px!important}}
  .kpi{{padding:12px 8px}} .kpi-value{{font-size:18px}} .section-note{{display:block;float:none;margin-top:3px}}
}}
</style>
</head>
<body>
<div class="preheader">Cache hit rate {hit_rate:.2f}% for the latest {window} window.</div>
<table class="shell" role="presentation" width="100%"><tr><td align="center">
<table class="container" role="presentation" width="680">
<tr><td class="header">
  <div class="brand">Bifrost &middot; Cache performance</div>
  <h1>Daily cache report</h1>
  <p class="period">{window} window<br>{period_start} &rarr; {period_end}</p>
</td></tr>
<tr><td class="content">
  <div class="summary">
    <div class="summary-label">Overall hit rate</div>
    <div class="summary-rate">{hit_rate_display}<span class="rate-status">{severity_label}</span></div>
    <div class="summary-context">{reason}</div>
  </div>
  <table class="kpis" role="presentation"><tr>
    <td class="kpi"><span class="kpi-label">Requests</span><span class="kpi-value">{self._count(overall['requests'])}</span></td>
    <td class="kpi"><span class="kpi-label">Cache hits</span><span class="kpi-value">{self._count(overall['hits'])}</span></td>
    <td class="kpi"><span class="kpi-label">Cache misses</span><span class="kpi-value">{self._count(overall['misses'])}</span></td>
  </tr></table>

  <div class="section">
    <div class="section-heading"><h2>Cache type breakdown</h2></div>
    <table class="compact-table" role="table">
      <thead><tr><th>Cache type</th><th style="text-align:right">Hits</th><th style="text-align:right">Share</th></tr></thead>
      <tbody>{''.join(type_rows)}</tbody>
    </table>
  </div>

  <div class="section">{details_table}</div>
</td></tr>
<tr><td class="footer">
  Generated from Bifrost Prometheus counters using <strong>{window}</strong> increases.<br>
  Counts can be fractional because Prometheus extrapolates counter values at window boundaries. Times are UTC.
  {detail_link}
</td></tr>
</table>
</td></tr></table>
</body>
</html>"""
