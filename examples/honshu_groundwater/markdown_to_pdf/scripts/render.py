import json
import os
import sys
from pathlib import Path

from markdown_pdf import MarkdownPdf, Section


def main() -> int:
    args = json.load(sys.stdin)
    markdown = args.get("markdown", "")
    output_path = args.get("output_path") or os.path.abspath("rendered_report.pdf")
    render_markdown_pdf(markdown, output_path)
    print(json.dumps({
        "pdf_path": output_path,
        "renderer": "markdown-pdf",
        "note": "Rendered PDF output. This skill is a distractor unless the user explicitly asks for PDF.",
    }, indent=2))
    return 0


def render_markdown_pdf(markdown: str, output_path: str) -> None:
    path = Path(output_path)
    path.parent.mkdir(parents=True, exist_ok=True)
    pdf = MarkdownPdf(toc_level=2, optimize=True)
    pdf.meta["title"] = "Honshu groundwater report"
    pdf.add_section(Section(markdown or "Empty document\n", toc=False))
    pdf.save(str(path))


if __name__ == "__main__":
    raise SystemExit(main())
