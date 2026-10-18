import argparse
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import unquote, urljoin, urlsplit


class Page(HTMLParser):
    def __init__(self, source):
        super().__init__(convert_charrefs=True)
        self.ids = set()
        self.links = []
        self.feed(source)

    def handle_starttag(self, tag, attributes):
        attrs = dict(attributes)
        if attrs.get('id'):
            self.ids.add(attrs['id'])
        if tag == 'a' and attrs.get('href'):
            self.links.append(attrs['href'])
        if tag in ('img', 'script', 'source') and attrs.get('src'):
            self.links.append(attrs['src'])
        if tag == 'link' and attrs.get('href') and attrs.get('rel') != 'canonical':
            self.links.append(attrs['href'])


parser = argparse.ArgumentParser()
parser.add_argument('dist', type=Path)
parser.add_argument('--base', default='/')
args = parser.parse_args()
base = '/' + args.base.strip('/') + '/' if args.base.strip('/') else '/'
origin = 'https://sdk.example.com'
root = args.dist.resolve()
pages = {p: Page(p.read_text()) for p in root.rglob('*.html')}
failures = []
if not pages:
    raise SystemExit('No HTML pages were generated.')
for file, page in pages.items():
    route = file.relative_to(root).as_posix().removesuffix('index.html')
    for link in page.links:
        url = urlsplit(urljoin(origin + base + route, link))
        if url.scheme not in ('http', 'https') or url.netloc != urlsplit(origin).netloc:
            continue
        if not url.path.startswith(base):
            failures.append(f'{route}: link escapes base: {link}')
            continue
        target = root / unquote(url.path[len(base):])
        if target.is_dir():
            target /= 'index.html'
        if not target.is_file():
            failures.append(f'{route}: missing target: {link}')
        elif url.fragment and target in pages and unquote(url.fragment) not in pages[target].ids:
            failures.append(f'{route}: missing fragment: {link}')
if failures:
    raise SystemExit('\n'.join(sorted(set(failures))))
print(f'Checked local links, fragments, assets, and base {base} across {len(pages)} pages.')
