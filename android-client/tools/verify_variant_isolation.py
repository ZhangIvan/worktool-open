#!/usr/bin/env python3
"""Offline source/optional APK isolation gate; no build, download, endpoint, server or device action.
APK mode requires an explicitly supplied existing official SDK apkanalyzer executable.
"""
import argparse,json,re,subprocess,zipfile,sys,xml.etree.ElementTree as ET
sys.dont_write_bytecode=True
from check_accessibility_manifest import source_check, check_manifest, check_service_xml
from pathlib import Path
ROOT=Path(__file__).resolve().parents[1]
A='{http://schemas.android.com/apk/res/android}'

def verify_sources():
    src=ROOT/'app/src';gradle=(ROOT/'app/build.gradle.kts').read_text()
    assert 'create("lab")' in gradle and 'applicationIdSuffix = ".lab"' in gradle
    assert 'initWith(getByName("debug"))' in gradle
    assert 'sourceSets' not in gradle, 'manual sourceSet merging requires separate review'
    main=ET.parse(src/'main/AndroidManifest.xml').getroot()
    assert main.find('application').get(A+'usesCleartextTraffic')=='false'
    lab=ET.parse(src/'lab/AndroidManifest.xml').getroot()
    assert lab.find('application').get(A+'usesCleartextTraffic')=='true'
    assert 'LAB CLEARTEXT' in lab.find('application').get(A+'label')
    source_check(ROOT)
    for variant,enabled in [('debug','true'),('release','true'),('lab','false')]:
        features=(src/variant/'java/io/worktool/open/prototype/policy/VariantFeatures.kt').read_text()
        assert re.search(r'const\s+val\s+ownFixtureAccessibility\s*=\s*'+enabled+r'\b',features)
    forbidden=['class LabEndpointPolicy','class LabFixtureCommandPolicy','class LabClientBudget','class MonotonicLeaseTimer','LAB CLEARTEXT','LAB FIXTURE PING','LAB FIXTURE NONCE']
    for variant in ['main','debug','release']:
        for f in (src/variant).rglob('*.kt'):
            text=f.read_text()
            assert not any(x in text for x in forbidden), f'lab implementation/marker leaked: {f}'
            assert not re.search(r'(?<![\w])(?:10|172|192)\.\d+\.\d+\.\d+',text), f'private literal leaked: {f}'
    for variant in ['debug','release']:
        text=(src/variant/'java/io/worktool/open/prototype/policy/VariantFactory.kt').read_text()
        assert 'HttpsEndpointPolicy()' in text and 'LabEndpointPolicy' not in text
    link=(src/'main/java/io/worktool/open/prototype/transport/OkHttpDeviceLink.kt').read_text()
    assert '.followRedirects(false).followSslRedirects(false)' in link
    assert 'routes.webSocketHandshake(identity.endpoint)' in link and 'routes.url(endpoint,path)' in link
    assert 'BuildConfig' not in link
    graph=(src/'main/java/io/worktool/open/prototype/WorkToolApplication.kt').read_text()
    assert re.search(r'OkHttpDeviceLink\(\s*endpointPolicy',graph)
    assert 'commandPolicy=VariantFactory.commandPolicy()' in graph
    engine=(src/'main/java/io/worktool/open/prototype/core/NativeCommandEngine.kt').read_text()
    accept=engine[engine.index('private fun accept('):]
    assert accept.index('commandPolicy.permits(body)') < accept.index('journal.accept(body)')
    assert accept.index('workBudget.command(permitEpoch,id)') < accept.index('journal.accept(body)')
    assert 'finally {slot.close()}' in graph
    assert 'budget.callback(epoch)' in graph
    assert link.index('budget.inboundFrame(epoch,decodedBytes)') < link.index('val j=JSONObject(text)')
    coretests=src/'test/java/io/worktool/open/prototype/core/NativeCommandEngineTest.kt'
    assert coretests.read_text().count('@Test')==19, 'retain the original 19 core tests'
    return {'source_isolation':'PASS','core_tests_preserved':19,'jvm_tests':'NOT_RUN','android_build':'NOT_RUN','apk_isolation':'NOT_RUN'}

def parse_resources(text):
    """Read aapt2 dump resources output, retaining exact IDs/names/default strings.
    Fail closed on ambiguous default values; localized package filters are forbidden.
    """
    entries={};current=None
    for line in text.splitlines():
        m=re.search(r'\bresource (0x[0-9a-fA-F]{8}) (?:[^ :]+:)?([A-Za-z0-9_]+/[A-Za-z0-9_]+)',line)
        if m:
            current=m.group(1).lower();assert current not in entries
            entries[current]={'name':m.group(2),'values':[]};continue
        if current and re.match(r'^\s*\([^)]*\)',line):
            entries[current]['values'].append(line.strip())
    assert entries, 'no parsed compiled resource IDs'
    return entries


def normalize_references(root,resources):
    for element in root.iter():
        for attr,value in list(element.attrib.items()):
            m=re.fullmatch(r'@(?:0x)?([0-9a-fA-F]{8})',value)
            if m:
                key='0x'+m.group(1).lower();assert key in resources,'unresolved compiled resource'
                element.set(attr,'@'+resources[key]['name'])


def resource_string(resources,name):
    found=[r for r in resources.values() if r['name']==name]
    assert len(found)==1 and len(found[0]['values'])==1, 'unique nonlocalized compiled package filter required'
    line=found[0]['values'][0]
    m=re.fullmatch(r'\(\)\s+(".*")',line)
    assert m, 'package filter must be a default literal string, not a reference'
    value=json.loads(m.group(1));assert isinstance(value,str)
    return value


def normalize_service_numbers(root):
    # Exact Android integer equivalents, not permissive bit-mask subset tests.
    values={'accessibilityEventTypes':(0x820,'typeWindowStateChanged|typeWindowContentChanged'),
            'accessibilityFeedbackType':(0x10,'feedbackGeneric'),
            'accessibilityFlags':(0x10,'flagReportViewIds'),
            'notificationTimeout':(100,'100')}
    for key,(number,symbolic) in values.items():
        value=root.get(A+key,'')
        if re.fullmatch(r'(?:0x[0-9a-fA-F]+|[0-9]+)',value):
            assert int(value,16 if value.startswith('0x') else 10)==number
            root.set(A+key,symbolic)


def resource_selftest():
    resources=parse_resources('resource 0x7f010001 string/ui_accessibility_package_filter\n  () "io.worktool.open.prototype"\nresource 0x7f020001 xml/ui_fixture_accessibility_service\n  () (file) res/xml/ui_fixture_accessibility_service.xml')
    assert resource_string(resources,'string/ui_accessibility_package_filter')=='io.worktool.open.prototype'
    xml=ET.fromstring('<x xmlns:android="http://schemas.android.com/apk/res/android" android:packageNames="@0x7f010001" android:accessibilityEventTypes="0x820"/>')
    normalize_references(xml,resources);normalize_service_numbers(xml)
    assert xml.get(A+'packageNames')=='@string/ui_accessibility_package_filter'
    assert xml.get(A+'accessibilityEventTypes')=='typeWindowStateChanged|typeWindowContentChanged'
    for bad in ['resource 0x7f010001 string/ui_accessibility_package_filter\n  () "*"\n  (en) "io.worktool.open.prototype"','resource 0x7f010001 string/ui_accessibility_package_filter\n  () @string/other']:
        try:resource_string(parse_resources(bad),'string/ui_accessibility_package_filter')
        except AssertionError:pass
        else:raise AssertionError('ambiguous/reference package filter accepted')
    return {'compiled_resource_parser_fixtures':'PASS','actual_apk_decode':'NOT_RUN'}


def verify_apk(variant,apk,apkanalyzer,aapt2):
    assert variant in ['debug','release','lab']
    apk=Path(apk).resolve();tool=Path(apkanalyzer).resolve()
    assert apk.is_file() and tool.is_file()
    # The caller must supply its existing official SDK tool; no PATH discovery or download.
    raw=subprocess.check_output([str(tool),'manifest','print',str(apk)],text=True)
    manifest=ET.fromstring(raw);app=manifest.find('application')
    resource_dump=subprocess.check_output([str(Path(aapt2).resolve()),'dump','resources',str(apk)],text=True)
    resources=parse_resources(resource_dump)
    normalize_references(manifest,resources)
    check_manifest(manifest,variant)
    if variant!='lab':
        service_raw=subprocess.check_output([str(tool),'resources','xml','--file','res/xml/ui_fixture_accessibility_service.xml',str(apk)],text=True)
        service_xml=ET.fromstring(service_raw)
        normalize_references(service_xml,resources)
        normalize_service_numbers(service_xml)
        package_value=resource_string(resources,'string/ui_accessibility_package_filter')
        check_service_xml(service_xml,package_value)
    if variant=='lab':
        assert manifest.get('package')=='io.worktool.open.prototype.lab'
        assert app.get(A+'usesCleartextTraffic')=='true'
    else:
        assert manifest.get('package')=='io.worktool.open.prototype'
        assert app.get(A+'usesCleartextTraffic')=='false'
    with zipfile.ZipFile(apk) as z:
        dex=b''.join(z.read(n) for n in z.namelist() if re.fullmatch(r'classes\d*\.dex',n))
        markers=[b'LabEndpointPolicy',b'LabFixtureCommandPolicy',b'LabClientBudget',b'MonotonicLeaseTimer',b'LAB CLEARTEXT',b'LAB FIXTURE NONCE']
        if variant=='lab':assert all(m in dex for m in markers)
        else:assert not any(m in dex for m in markers), 'lab bytecode or fixture marker leaked'
    return {'variant':variant,'apk_isolation':'PASS','compiled_manifest':'PASS','compiled_service_filter':'PASS' if variant!='lab' else 'NOT_REGISTERED','shared_accessibility_dex':'NOT_A_REACHABILITY_PROOF'}

if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--apk',action='append',default=[],help='variant=/absolute/path.apk');p.add_argument('--apkanalyzer');p.add_argument('--aapt2');p.add_argument('--selftest',action='store_true')
    args=p.parse_args();result=resource_selftest() if args.selftest else verify_sources()
    if args.apk:
        assert args.apkanalyzer and args.aapt2, 'provide existing official SDK apkanalyzer and aapt2; no download'
        assert Path(args.aapt2).is_file()
        entries=[entry.split('=',1) for entry in args.apk]
        assert len(entries)==3 and {entry[0] for entry in entries}=={'debug','release','lab'}, 'all three distinct variants are required'
        result['artifacts']=[verify_apk(*entry,args.apkanalyzer,args.aapt2) for entry in entries]
        result['apk_isolation']='PASS'
    print(json.dumps(result,indent=2))
