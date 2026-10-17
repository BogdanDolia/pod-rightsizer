#!/usr/bin/env python3
"""Validate the buildless website's local assets and fragment links."""

from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import unquote, urlsplit


class Page(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.ids = set()
        self.references = []

    def handle_starttag(self, tag, attrs):
        values = dict(attrs)
        if "id" in values:
            if values["id"] in self.ids:
                raise ValueError(f"Duplicate id: {values['id']}")
            self.ids.add(values["id"])
        for key in ("href", "src"):
            if values.get(key):
                self.references.append(values[key])


def main():
    root = Path(__file__).resolve().parents[1] / "site"
    pages = {}
    for path in root.rglob("*.html"):
        page = Page()
        page.feed(path.read_text())
        pages[path.resolve()] = page
    if (root / "index.html").resolve() not in pages:
        raise ValueError("site/index.html is required")
    for path, page in pages.items():
        for reference in page.references:
            url = urlsplit(reference)
            if url.scheme or url.netloc:
                continue
            if url.path.startswith("/"):
                raise ValueError(f"Use relative URLs for project Pages: {reference}")
            target = (path.parent / unquote(url.path)).resolve() if url.path else path
            if not target.is_relative_to(root.resolve()) or not target.is_file():
                raise ValueError(f"Missing or out-of-site target: {reference}")
            if url.fragment and target in pages:
                if unquote(url.fragment) not in pages[target].ids:
                    raise ValueError(f"Missing fragment: {reference}")
    print(f"Website links and assets passed ({len(pages)} HTML page(s))")


if __name__ == "__main__":
    main()
