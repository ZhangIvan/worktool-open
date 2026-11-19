#!/usr/bin/env python3
"""Keep bounded JUnit outcomes and lint counts, never report bodies/paths/properties or runtime data."""
from pathlib import Path
import json, re, xml.etree.ElementTree as ET

ROOT = Path(__file__).resolve().parents[1]
SAFE = re.compile(r'[A-Za-z0-9_.$() -]{1,160}\Z')


def collect(root=ROOT):
    build = root / 'app/build'
    out = root / 'ci-artifact'
    dest = out / 'jvm-test-results'
    dest.mkdir(parents=True, exist_ok=True)
    summaries, lint_summaries = {}, {}
    for variant in ('debug', 'release', 'lab'):
        reports = sorted((build / ('test-results/test' + variant.title() + 'UnitTest')).glob('TEST-*.xml'))
        assert 1 <= len(reports) <= 20, 'JVM XML missing or count exceeds limit: ' + variant
        summary = dict(suites=0, tests=0, failures=0, errors=0, skipped=0)
        for index, path in enumerate(reports):
            assert path.stat().st_size <= 1048576, 'JVM XML exceeds 1 MiB'
            original = ET.parse(path).getroot()
            assert original.tag == 'testsuite'
            clean = ET.Element('testsuite')
            for key in ('tests', 'failures', 'errors', 'skipped', 'time'):
                value = original.get(key, '0')
                assert re.fullmatch(r'[0-9]+(?:\.[0-9]+)?', value)
                clean.set(key, value)
                if key != 'time':
                    summary[key] += int(value)
            name = original.get('name', 'suite')
            clean.set('name', name if SAFE.fullmatch(name) else 'redacted_suite')
            for case in original.findall('testcase'):
                attrs = {}
                for key in ('name', 'classname'):
                    value = case.get(key, 'unknown')
                    attrs[key] = value if SAFE.fullmatch(value) else 'redacted'
                duration = case.get('time', '0')
                attrs['time'] = duration if re.fullmatch(r'[0-9]+(?:\.[0-9]+)?', duration) else '0'
                item = ET.SubElement(clean, 'testcase', attrs)
                for tag in ('failure', 'error', 'skipped'):
                    if case.find(tag) is not None:
                        ET.SubElement(item, tag)
            ET.ElementTree(clean).write(dest / f'TEST-{variant}-{index:02d}.xml', encoding='utf-8', xml_declaration=True)
            summary['suites'] += 1
        assert summary['tests'] > 0
        summaries[variant] = summary
        lint = build / f'reports/lint-results-{variant}.xml'
        assert lint.is_file() and lint.stat().st_size <= 2097152, 'Lint XML missing or oversized'
        counts = {}
        for issue in ET.parse(lint).getroot().findall('issue'):
            issue_id, severity = issue.get('id', 'unknown'), issue.get('severity', 'unknown')
            assert SAFE.fullmatch(issue_id) and SAFE.fullmatch(severity)
            key = issue_id + ':' + severity
            counts[key] = counts.get(key, 0) + 1
        lint_summaries[variant] = counts
    (out / 'jvm-summary.json').write_text(json.dumps({'schemaVersion': 2, 'source': 'actual Gradle JUnit XML',
        'note': 'tests includes skipped; shared cases repeat across variants, not unique capability counts',
        'redaction': 'properties, streams, stack/failure messages and paths omitted', 'variants': summaries}, indent=2) + '\n')
    (out / 'lint-summary.json').write_text(json.dumps({'schemaVersion': 2, 'source': 'actual variant lint XML',
        'redaction': 'only rule ID, severity and counts', 'variants': lint_summaries}, indent=2) + '\n')


if __name__ == '__main__':
    collect()
