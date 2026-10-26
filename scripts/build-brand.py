#!/usr/bin/env python3
"""Build flat SwarmMind SVG assets from the approved mark geometry.

No runtime dependencies. wordmark-outline.svg is the checked-in outline master;
font installation is not required to build or display the exported assets.
PNG exports are rasterized from these SVGs, not from the concept board.
"""
from pathlib import Path
import math
import xml.etree.ElementTree as ET

ROOT = Path(__file__).resolve().parents[1]
OUT = ROOT / 'assets' / 'brand'
INDIGO, TEAL, INK = '#4F46E5', '#22D3C5', '#172033'
# Four modules retain the approved concept's silhouette and central negative space.
MODULES = [
    (INDIGO, [(116, 244), (288, 146), (406, 214), (406, 290), (288, 232), (190, 289), (190, 296), (116, 253)]),
    (TEAL, [(90, 255), (232, 338), (195, 372), (229, 397), (208, 410), (90, 342)]),
    (INDIGO, [(248, 265), (376, 336), (376, 411), (248, 337)]),
    (INDIGO, [(116, 396), (192, 434), (281, 378), (360, 419), (194, 511), (116, 468)]),
]


def rounded(points, radius=8):
    corners = []
    for i, p in enumerate(points):
        before, after = points[i-1], points[(i+1) % len(points)]
        a, b = math.dist(p, before), math.dist(p, after)
        r = min(radius, a/3, b/3)
        enter = (p[0]+(before[0]-p[0])*r/a, p[1]+(before[1]-p[1])*r/a)
        leave = (p[0]+(after[0]-p[0])*r/b, p[1]+(after[1]-p[1])*r/b)
        corners.append((enter, p, leave))
    def xy(p):
        return f'{p[0]:.3f} {p[1]:.3f}'
    d = 'M' + xy(corners[0][0])
    for i, (a, p, b) in enumerate(corners):
        if i:
            d += 'L' + xy(a)
        d += 'Q' + xy(p) + ' ' + xy(b)
    return d + 'Z'


def mark(color=None):
    return '\n'.join(f'<path fill="{color or c}" d="{rounded(ps)}"/>' for c, ps in MODULES)


def svg(width, height, body, label, view=None):
    return f'''<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" viewBox="{view or f'0 0 {width} {height}'}" role="img" aria-label="{label}">
<title>{label}</title>
{body}
</svg>
'''


def main():
    OUT.mkdir(parents=True, exist_ok=True)
    for name, color in [('logo-mark', None), ('logo-mono', INK), ('logo-white', '#FFFFFF')]:
        (OUT / f'{name}.svg').write_text(svg(512, 512, mark(color), 'SwarmMind symbol', '48 128 400 400'))
    font = ET.parse(OUT / 'wordmark-outline.svg').getroot()
    width = float(font.attrib['viewBox'].split()[2])
    paths = ''.join(f'<path d="{p.attrib["d"]}"/>' for p in font.iter('{http://www.w3.org/2000/svg}path'))
    scale = 820 / width
    def lockup(fg):
        return (f'<g transform="translate(30 20) scale(.65) translate(-48 -128)">{mark()}</g>'
                f'<g fill="{fg}" transform="translate(330 60) scale({scale:.6f})">{paths}</g>')
    (OUT / 'logo-lockup.svg').write_text(svg(1200, 300, lockup(INK), 'SwarmMind'))
    for name, bg, fg in [('logo-github', '#FFFFFF', INK), ('logo-github-dark', INK, '#FFFFFF')]:
        content = f'<rect width="1400" height="400" rx="24" fill="{bg}"/>'
        content += f'<g transform="translate(100 50)">{lockup(fg)}</g>'
        (OUT / f'{name}.svg').write_text(svg(1400, 400, content, 'SwarmMind'))
    print('Built six SVG exports in assets/brand/')


if __name__ == '__main__':
    main()
