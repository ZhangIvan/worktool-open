#!/usr/bin/env python3
"""Offline source/optional APK isolation gate; no build, download, endpoint, server or device action.
APK mode requires an explicitly supplied existing official SDK apkanalyzer executable.
"""
import argparse,json,re,subprocess,zipfile,xml.etree.ElementTree as ET
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
    for variant in ['main','debug','release','lab']:
        manifest=src/variant/'AndroidManifest.xml'
        if manifest.exists():
            assert not ET.parse(manifest).getroot().findall('.//service'), 'no service registrations permitted'
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
    assert 'OkHttpDeviceLink(endpointPolicy)' in graph
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

def verify_apk(variant,apk,apkanalyzer):
    assert variant in ['debug','release','lab']
    apk=Path(apk).resolve();tool=Path(apkanalyzer).resolve()
    assert apk.is_file() and tool.is_file()
    # The caller must supply its existing official SDK tool; no PATH discovery or download.
    raw=subprocess.check_output([str(tool),'manifest','print',str(apk)],text=True)
    manifest=ET.fromstring(raw);app=manifest.find('application')
    assert not manifest.findall('.//service')
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
    return {'variant':variant,'apk_isolation':'PASS'}

if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--apk',action='append',default=[],help='variant=/absolute/path.apk');p.add_argument('--apkanalyzer')
    args=p.parse_args();result=verify_sources()
    if args.apk:
        assert args.apkanalyzer, 'provide an existing official SDK apkanalyzer'
        entries=[entry.split('=',1) for entry in args.apk]
        assert len(entries)==3 and {entry[0] for entry in entries}=={'debug','release','lab'}, 'all three distinct variants are required'
        result['artifacts']=[verify_apk(*entry,args.apkanalyzer) for entry in entries]
        result['apk_isolation']='PASS'
    print(json.dumps(result,indent=2))
