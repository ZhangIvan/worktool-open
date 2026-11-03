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

ORIGINAL_CORE_TESTS = {
    'conflictAndReverseDirectionFailClosed',
    'defaultPausedRejectsWithoutEffect',
    'duplicateAckReceiptCannotClearAnotherPendingResult',
    'expiredDoesNotExecute',
    'exportRequiresExplicitMatchingLocalScope',
    'failedStartedSendDisconnectsBeforeAnyEffectAndPersistsFailure',
    'interruptedRunningRecoversUnknownWithoutReplay',
    'jcsOrderIndependentButScopeArrayOrderBound',
    'onlySentDurableResultCanBeAcknowledged',
    'pauseRejectsAll101AcceptedRowsBeyondRecentUiLimit',
    'reconnectRejectsAll101AcceptedRowsBeyondRecentUiLimit',
    'revokedPolicyPublishesPausedGateAndCannotResumeLocally',
    'staleSentAtRejectsBeforeAcceptance',
    'stopBeforeConfirmationCancelsWithoutEffect',
    'textEffectDurableAndDuplicateDoesNotRepeat',
    'validCommandWithTenSecondsRemainingMayExecute',
    'wrongAckEventIdCannotClearDurableResult',
    'wrongIdentityAndExtraFieldsFailClosed',
    'wrongReferenceOrArbitraryPathRejected',
}
INTEROP_CORE_TESTS = {
    'sharedGoExchangeExecutesOnceAndAcknowledgesExactResult',
    'sharedUnackedResultSurvivesEngineReplacementWithoutEffectReplay',
}

def check_core_test_inventory(text):
    names=re.findall(r'@Test\s+fun\s+(\w+)\s*\(',text)
    assert len(names)==len(set(names))==21, 'exact 21 distinct core test methods required'
    assert text.count('@Test')==21, 'unsupported or unparsed core test annotation'
    assert set(names)==ORIGINAL_CORE_TESTS | INTEROP_CORE_TESTS, 'original 19 and reviewed 2 interop tests required'

def core_inventory_selftest():
    names=sorted(ORIGINAL_CORE_TESTS | INTEROP_CORE_TESTS)
    source='\n'.join('@Test fun '+name+'() {}' for name in names)
    check_core_test_inventory(source)
    cases=[source.replace('@Test fun '+names[0]+'() {}',''),
           source.replace(names[0], 'unreviewedReplacement'),
           source.replace(names[0],names[1]),
           source+'\n@Test fun unreviewedAddition() {}']
    for bad in cases:
        try: check_core_test_inventory(bad)
        except AssertionError: pass
        else: raise AssertionError('core inventory negative accepted')

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
    check_core_test_inventory(coretests.read_text())
    return {'source_isolation':'PASS','core_tests_preserved':19,'interop_tests_present':2,'jvm_tests':'NOT_RUN','android_build':'NOT_RUN','apk_isolation':'NOT_RUN'}

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


# Official format source: Android tools/base commit
# 6a81855c2fa102ae4532ad9a645e40177770a26a ResourceIdResolver.NO_RESOLUTION
# emits @ref/0x%x. Still require the exact ID in this APK's aapt2 table.
def normalize_reference_attributes(attributes,resources):
    for element,attr,location in attributes:
        value=element.get(attr,'')
        m=re.fullmatch(r'@(?:ref/0x|0x)?([0-9a-fA-F]{8})',value)
        if m:
            key='0x'+m.group(1).lower()
            # Only fixed caller-owned location + numeric resource ID; no values.
            assert key in resources, 'unresolved consumed resource: '+location+' '+key
            element.set(attr,'@'+resources[key]['name'])
        elif re.fullmatch(r'@[A-Za-z0-9_]+/[A-Za-z0-9_]+',value):
            matches=[r for r in resources.values() if r['name']==value[1:]]
            assert len(matches)==1, 'unresolved consumed symbolic resource: '+location
        # Unsupported syntax remains unchanged and fails exact semantic checker.


def normalize_references(root,resources):
    # Do not resolve unrelated framework theme/icon references via app resources.
    # These are the ONLY reference-valued attributes consumed by security checks.
    attributes=[]
    if root.tag=='manifest':
        for service in root.findall('application/service'):
            for meta in service.findall('meta-data'):
                if meta.get(A+'name')=='android.accessibilityservice':
                    attributes.append((meta,A+'resource','service.metadata.resource'))
    elif root.tag=='accessibility-service':
        attributes=[(root,A+name,'service.xml.'+name) for name in ('description','packageNames')]
    else:
        raise AssertionError('unsupported reference normalization root')
    normalize_reference_attributes(attributes,resources)


def resource_string(resources,name):
    found=[r for r in resources.values() if r['name']==name]
    assert len(found)==1 and len(found[0]['values'])==1, 'unique nonlocalized compiled package filter required'
    line=found[0]['values'][0]
    m=re.fullmatch(r'\(\)\s+(".*")',line)
    assert m, 'package filter must be a default literal string, not a reference'
    value=json.loads(m.group(1));assert isinstance(value,str)
    return value


def resource_file(resources,name,members):
    # Use the exact named XML record selected by the validated metadata, never a
    # source-tree filename. Optimized release APK members may have shorter names.
    assert name=='xml/ui_fixture_accessibility_service', 'unexpected XML resource type/name'
    found=[r for r in resources.values() if r['name']==name]
    assert len(found)==1 and len(found[0]['values'])==1, 'unique default XML file mapping required'
    line=found[0]['values'][0]
    m=re.fullmatch(r'\(\)\s+\(file\)\s+(res/[A-Za-z0-9_./-]+)(?:\s+type=XML)?',line)
    assert m, 'XML resource must be a default APK file mapping'
    path=m.group(1)
    assert len(path)<=256 and all(part not in ('','.','..') for part in path.split('/')), 'unsafe APK XML member path'
    matches=[item for item in members if item.filename==path]
    assert len(matches)==1, 'XML APK member missing or duplicate'
    item=matches[0]
    assert not item.is_dir() and (item.external_attr>>16)&0o170000!=0o120000, 'XML member is not regular data'
    assert 0<item.file_size<=1048576, 'XML APK member size outside bound'
    return path


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
    core_inventory_selftest()
    resources=parse_resources('resource 0x7f010001 string/ui_accessibility_package_filter\n  () "io.worktool.open.prototype"\nresource 0x7f020001 xml/ui_fixture_accessibility_service\n  () (file) res/xml/ui_fixture_accessibility_service.xml')
    assert resource_string(resources,'string/ui_accessibility_package_filter')=='io.worktool.open.prototype'
    xml=ET.fromstring('<x xmlns:android="http://schemas.android.com/apk/res/android" android:packageNames="@0x7f010001" android:accessibilityEventTypes="0x820"/>')
    normalize_reference_attributes([(xml,A+'packageNames','fixture.packageNames')],resources);normalize_service_numbers(xml)
    assert xml.get(A+'packageNames')=='@string/ui_accessibility_package_filter'
    assert xml.get(A+'accessibilityEventTypes')=='typeWindowStateChanged|typeWindowContentChanged'
    for bad in ['resource 0x7f010001 string/ui_accessibility_package_filter\n  () "*"\n  (en) "io.worktool.open.prototype"','resource 0x7f010001 string/ui_accessibility_package_filter\n  () @string/other']:
        try:resource_string(parse_resources(bad),'string/ui_accessibility_package_filter')
        except AssertionError:pass
        else:raise AssertionError('ambiguous/reference package filter accepted')
    # Official no-resolver format must resolve only through an exact table ID.
    for raw in ['@ref/0x7f020001','@ref/0x7F020001','@0x7f020001','@7f020001']:
        x=ET.Element('meta-data',{A+'resource':raw})
        normalize_reference_attributes([(x,A+'resource','fixture.resource')],resources)
        assert x.get(A+'resource')=='@xml/ui_fixture_accessibility_service'
    for raw in ['@ref/0x7f020099','@0x7f020099']:
        try:normalize_reference_attributes([(ET.Element('x',{A+'resource':raw}),A+'resource','fixture.resource')],resources)
        except AssertionError:pass
        else:raise AssertionError('unknown resource ID accepted')
    for raw in ['@dref/0x7f020001','@ref/0x7f020001suffix','@ref/0x7f020001\n','@ref/2130837505','@wrong/0x7f020001']:
        x=ET.Element('x',{A+'resource':raw})
        try:normalize_reference_attributes([(x,A+'resource','fixture.resource')],resources)
        except AssertionError:pass
        else:assert x.get(A+'resource')!='@xml/ui_fixture_accessibility_service'
    x=ET.Element('x',{A+'resource':'@ref/0x7f010001'});normalize_reference_attributes([(x,A+'resource','fixture.resource')],resources)
    assert x.get(A+'resource')=='@string/ui_accessibility_package_filter' # wrong type is NOT aliased to xml
    for raw,accepted in [('@ref/0x7f020001',True),('@ref/0x7f010001',False),('@dref/0x7f020001',False)]:
        manifest=ET.parse(ROOT/'app/src/main/AndroidManifest.xml').getroot()
        manifest.set('package','io.worktool.open.prototype')
        manifest.find('application/service/meta-data').set(A+'resource',raw)
        try:
            normalize_references(manifest,resources)
            check_manifest(manifest,'debug')
        except AssertionError:assert not accepted
        else:assert accepted
    # Unconsumed framework refs are preserved; consumed missing refs still fail.
    manifest=ET.parse(ROOT/'app/src/main/AndroidManifest.xml').getroot()
    manifest.set('package','io.worktool.open.prototype')
    manifest.find('application').set(A+'theme','@ref/0x01030224')
    meta=manifest.find('application/service/meta-data')
    meta.set(A+'resource','@ref/0x7f020001')
    normalize_references(manifest,resources);check_manifest(manifest,'debug')
    assert manifest.find('application').get(A+'theme')=='@ref/0x01030224'
    for raw in ['@ref/0x01030224','@ref/0x7f020099','@xml/missing']:
        meta.set(A+'resource',raw)
        try:normalize_references(manifest,resources)
        except AssertionError:pass
        else:raise AssertionError('missing consumed resource accepted')
    service=ET.Element('accessibility-service',{A+'description':'@ref/0x7f010099',A+'packageNames':'@ref/0x7f010001'})
    try:normalize_references(service,resources)
    except AssertionError as error:assert 'service.xml.description' in str(error)
    else:raise AssertionError('missing consumed description accepted')
    for target,attribute in [('application','usesCleartextTraffic'),('application','allowBackup'),('application/service','permission'),('application/service','exported')]:
        m=ET.parse(ROOT/'app/src/main/AndroidManifest.xml').getroot();m.set('package','io.worktool.open.prototype')
        m.find(target).set(A+attribute,'@ref/0x7f010001')
        normalize_references(m,resources)
        try:check_manifest(m,'debug')
        except AssertionError:pass
        else:raise AssertionError('referenced security attribute accepted')
    for attribute in ['accessibilityEventTypes','accessibilityFlags','canRetrieveWindowContent','canPerformGestures','isAccessibilityTool']:
        sx=ET.parse(ROOT/'app/src/main/res/xml/ui_fixture_accessibility_service.xml').getroot()
        sx.set(A+attribute,'@ref/0x7f010001')
        try:check_service_xml(sx,'io.worktool.open.prototype')
        except AssertionError:pass
        else:raise AssertionError('referenced service security attribute accepted')
    diag=diagnostic_reference('@ref/0x7f020001',resources)
    assert diag['metadata_resource_repr']==repr('@ref/0x7f020001')
    assert all(set(r)=={'id','type','name'} for r in diag['resource_identity_candidates'])
    assert diagnostic_reference('secret payload with spaces',resources)['metadata_resource_repr']=='ABSENT_OR_REDACTED'
    def xml_resources(value):
        return {'0x7f020001':{'name':'xml/ui_fixture_accessibility_service','values':[value]}}
    def member(name):
        info=zipfile.ZipInfo(name);info.file_size=64;return info
    for path in ['res/xml/ui_fixture_accessibility_service.xml','res/a.xml','res/Ab']:
        for suffix in ['', ' type=XML']:
            assert resource_file(xml_resources('() (file) '+path+suffix),'xml/ui_fixture_accessibility_service',[member(path)])==path
    for value in ['(en) (file) res/a.xml','() @xml/other','() (file) /res/a.xml','() (file) ../a.xml','() (file) res/../a.xml','() (file) res//a.xml','() (file) res/./a.xml','() (file) https://example.test/a.xml','() (file) res/a.xml type=PNG','() (file) res/a.xml?query','() (file) res/a%2exml','() (file) res/a\\b.xml']:
        try:resource_file(xml_resources(value),'xml/ui_fixture_accessibility_service',[member('res/a.xml')])
        except AssertionError:pass
        else:raise AssertionError('unsafe/ambiguous XML file mapping accepted')
    for members in [[],[member('res/a.xml'),member('res/a.xml')]]:
        try:resource_file(xml_resources('() (file) res/a.xml'),'xml/ui_fixture_accessibility_service',members)
        except AssertionError:pass
        else:raise AssertionError('missing/duplicate XML member accepted')
    for records in [{}, {'0x7f020001':{'name':'xml/ui_fixture_accessibility_service','values':['() (file) res/a.xml','(en) (file) res/b.xml']}}, {'0x7f020001':{'name':'string/ui_fixture_accessibility_service','values':['() (file) res/a.xml']}}, dict(xml_resources('() (file) res/a.xml'),other={'name':'xml/ui_fixture_accessibility_service','values':['() (file) res/a.xml']})]:
        try:resource_file(records,'xml/ui_fixture_accessibility_service',[member('res/a.xml')])
        except AssertionError:pass
        else:raise AssertionError('missing/ambiguous/wrong-type resource accepted')
    return {'compiled_resource_parser_fixtures':'PASS','actual_apk_decode':'NOT_RUN'}


def metadata_reference(root):
    services=root.findall('application/service')
    values=[m.get(A+'resource','') for service in services
            for m in service.findall('meta-data')
            if m.get(A+'name')=='android.accessibilityservice']
    return values[0] if len(values)==1 else None


def diagnostic_reference(value,resources):
    # Exact resource syntax only: never dump arbitrary XML text/resource values.
    safe=isinstance(value,str) and len(value)<=160 and re.fullmatch(r'@[A-Za-z0-9_./:]+',value)
    raw=repr(value) if safe else 'ABSENT_OR_REDACTED'
    match=re.fullmatch(r'@(?:ref/0x|0x)?([0-9a-fA-F]{8})',value or '')
    key='0x'+match.group(1).lower() if match else None
    records=[]
    for rid,record in resources.items():
        if rid==key or record['name']=='xml/ui_fixture_accessibility_service':
            name=record['name']
            if re.fullmatch(r'[A-Za-z0-9_]+/[A-Za-z0-9_]+',name):
                kind,entry=name.split('/',1)
                records.append({'id':rid,'type':kind,'name':entry})
    return {'metadata_resource_repr':raw,'resource_identity_candidates':records[:4]}


def tool_version(tool, arguments):
    # Failure-only bounded public version probe; no environment or SDK paths.
    try:
        p=subprocess.run([str(tool),*arguments],capture_output=True,text=True,timeout=10)
        value=p.stdout.strip()
        if p.returncode==0 and 0<len(value)<=160 and re.fullmatch(r'[A-Za-z0-9 ._()+:/\-\n]+',value):
            return value
    except (OSError,subprocess.TimeoutExpired):
        pass
    return 'UNAVAILABLE'


def verify_apk(variant,apk,apkanalyzer,aapt2):
    assert variant in ['debug','release','lab']
    apk=Path(apk).resolve();tool=Path(apkanalyzer).resolve()
    assert apk.is_file() and tool.is_file()
    # The caller must supply its existing official SDK tool; no PATH discovery or download.
    raw=subprocess.check_output([str(tool),'manifest','print',str(apk)],text=True)
    manifest=ET.fromstring(raw);app=manifest.find('application')
    resource_dump=subprocess.check_output([str(Path(aapt2).resolve()),'dump','resources',str(apk)],text=True)
    resources=parse_resources(resource_dump)
    original_metadata=metadata_reference(manifest)
    try:
        normalize_references(manifest,resources)
        check_manifest(manifest,variant)
    except AssertionError:
        diagnostic=diagnostic_reference(original_metadata,resources)
        diagnostic.update(variant=variant,stage='MANIFEST_REFERENCE_GATE',
                          apkanalyzer_version=tool_version(tool,['--version']),
                          aapt2_version=tool_version(Path(aapt2).resolve(),['version']))
        print(json.dumps(diagnostic),file=sys.stderr)
        raise
    service_file=None
    if variant!='lab':
        # check_manifest above already proved metadata resolves to this exact name.
        with zipfile.ZipFile(apk) as archive:
            service_file=resource_file(resources,'xml/ui_fixture_accessibility_service',archive.infolist())
        print(json.dumps({'variant':variant,'stage':'COMPILED_SERVICE_XML_MEMBER','resource':'xml/ui_fixture_accessibility_service','apk_member':service_file}),file=sys.stderr)
        service_raw=subprocess.check_output([str(tool),'resources','xml','--file',service_file,str(apk)],text=True)
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
    return {'variant':variant,'apk_isolation':'PASS','compiled_manifest':'PASS','compiled_service_filter':'PASS' if variant!='lab' else 'NOT_REGISTERED','shared_accessibility_dex':'NOT_A_REACHABILITY_PROOF','service_xml_apk_member':service_file,'metadata_reference':diagnostic_reference(original_metadata,resources) if variant!='lab' else 'NOT_REGISTERED'}

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
