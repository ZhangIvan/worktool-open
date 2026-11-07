#!/usr/bin/env python3
"""Read-only source or decoded APK gate. Never grants permission or runs a device.
APK decode is done by verify_variant_isolation using an explicit official tool.
Source and pure fixture checks are NOT compiled-resource evidence.
"""
import argparse
import copy
import json
import xml.etree.ElementTree as ET
from pathlib import Path
A = '{http://schemas.android.com/apk/res/android}'
T = '{http://schemas.android.com/tools}'
BASE = 'io.worktool.open.prototype'
SERVICE = BASE + '.accessibility.OwnFixtureAccessibilityService'
FIXTURE = BASE + '.ui.FixtureActivity'
META = 'android.accessibilityservice'


def qualified(name, package=BASE):
    return package + name if name.startswith('.') else name


def check_manifest(root, variant, compiled=True):
    assert variant in ('debug', 'release', 'lab')
    package = BASE + ('.lab' if variant == 'lab' else '')
    if compiled:
        assert root.get('package') == package
    app = root.find('application'); assert app is not None
    permissions = [n.get(A+'name') for n in root if n.tag.startswith('uses-permission')]
    assert permissions == ['android.permission.INTERNET'], 'unexpected permission surface'
    assert not root.findall('permission'), 'no custom permission declaration'
    assert app.get(A+'usesCleartextTraffic') == ('true' if variant == 'lab' else 'false')
    assert app.get(A+'allowBackup') == 'false'
    assert app.get(A+'fullBackupContent') == 'false'
    assert not app.findall('receiver') and not app.findall('activity-alias')
    for p in app.findall('provider'):
        assert p.get(A+'exported') == 'false'
    fixtures = [x for x in app.findall('activity') if qualified(x.get(A+'name',''), package) == FIXTURE]
    services = app.findall('service')
    if variant == 'lab':
        assert not services and not fixtures, 'LAB must have no registered service or Fixture activity'
    else:
        assert len(services) == 1
        s = services[0]
        assert qualified(s.get(A+'name',''), package) == SERVICE
        assert s.get(A+'permission') == 'android.permission.BIND_ACCESSIBILITY_SERVICE'
        assert s.get(A+'exported') == 'true'
        assert [x.get(A+'name') for x in s.findall('intent-filter/action')] == ['android.accessibilityservice.AccessibilityService']
        assert len(s.findall('intent-filter')) == 1
        assert not s.findall('intent-filter/category') and not s.findall('intent-filter/data')
        metas = s.findall('meta-data'); assert len(metas) == 1
        assert metas[0].get(A+'name') == META
        assert metas[0].get(A+'resource') == '@xml/ui_fixture_accessibility_service'
        assert len(fixtures) == 1 and fixtures[0].get(A+'exported') == 'false'
        assert not fixtures[0].findall('intent-filter')
    return {'variant':variant, 'manifest':'PASS', 'compiled':compiled,
            'service_registered':variant != 'lab', 'device_permission':'NOT_TESTED'}


def check_service_xml(root, resolved_package=None):
    assert root.tag == 'accessibility-service'
    expected = {
        'description':'@string/ui_accessibility_service_description',
        'packageNames':'@string/ui_accessibility_package_filter',
        'accessibilityEventTypes':'typeWindowStateChanged|typeWindowContentChanged',
        'accessibilityFeedbackType':'feedbackGeneric',
        'accessibilityFlags':'flagReportViewIds',
        'notificationTimeout':'100',
        'canRetrieveWindowContent':'true',
        'canPerformGestures':'false',
        'isAccessibilityTool':'false',
    }
    assert set(root.attrib) == {A+k for k in expected}, 'unreviewed accessibility XML attributes'
    for k,v in expected.items():
        actual = root.get(A+k)
        if k == 'accessibilityEventTypes':
            assert set(actual.split('|')) == set(v.split('|'))
        else:
            assert actual == v, 'accessibility metadata mismatch: ' + k
    if resolved_package is not None:
        assert resolved_package == BASE, 'compiled package filter must be exact secure applicationId'
    assert not list(root)


def source_check(root):
    src=root/'app/src'
    manifest=ET.parse(src/'main/AndroidManifest.xml').getroot()
    check_manifest(manifest,'debug',compiled=False)
    lab=ET.parse(src/'lab/AndroidManifest.xml').getroot().find('application')
    assert lab.get(A+'usesCleartextTraffic')=='true'
    assert 'LAB CLEARTEXT' in lab.get(A+'label','')
    assert [(x.get(A+'name'),x.get(T+'node')) for x in lab.findall('service')]==[(SERVICE,'remove')]
    assert [(x.get(A+'name'),x.get(T+'node')) for x in lab.findall('activity')]==[(FIXTURE,'remove')]
    check_service_xml(ET.parse(src/'main/res/xml/ui_fixture_accessibility_service.xml').getroot())
    gradle=(root/'app/build.gradle.kts').read_text()
    assert 'variant.applicationId.map' in gradle and 'ui_accessibility_package_filter' in gradle
    return {'source_manifest_policy':'PASS','compiled_apk':'NOT_RUN','resolved_package_filter':'NOT_RUN'}


def selftest():
    root=Path(__file__).resolve().parents[1]
    source_check(root)
    secure=ET.parse(root/'app/src/main/AndroidManifest.xml').getroot();secure.set('package',BASE)
    check_manifest(secure,'debug')
    mutations=[lambda x:x.find('application/service').set(A+'permission',''),
               lambda x:x.find('application/service').set(A+'exported','false'),
               lambda x:x.find('application/activity').set(A+'exported','true'),
               lambda x:ET.SubElement(x,'uses-permission',{A+'name':'android.permission.READ_CONTACTS'})]
    for mutation in mutations:
        v=copy.deepcopy(secure);mutation(v)
        try: check_manifest(v,'debug')
        except AssertionError: pass
        else: raise AssertionError('unsafe manifest accepted')
    lab=copy.deepcopy(secure);lab.set('package',BASE+'.lab');app=lab.find('application');app.set(A+'usesCleartextTraffic','true')
    for x in list(app):
        if x.tag=='service' or (x.tag=='activity' and x.get(A+'name')==FIXTURE):app.remove(x)
    check_manifest(lab,'lab')
    ET.SubElement(app,'service',{A+'name':SERVICE})
    try:check_manifest(lab,'lab')
    except AssertionError:pass
    else:raise AssertionError('LAB service accepted')
    xml=ET.parse(root/'app/src/main/res/xml/ui_fixture_accessibility_service.xml').getroot()
    for key,value in [('isAccessibilityTool','true'),('canPerformGestures','true'),('packageNames','*'),('accessibilityFlags','flagRetrieveInteractiveWindows')]:
        v=copy.deepcopy(xml);v.set(A+key,value)
        try:check_service_xml(v,BASE)
        except AssertionError:pass
        else:raise AssertionError('unsafe metadata accepted')
    try:check_service_xml(xml,BASE+'.lab')
    except AssertionError:pass
    else:raise AssertionError('wrong resolved package accepted')
    return {'pure_fixture_tests':'PASS','apk_decode':'NOT_RUN','device':'NOT_RUN'}


if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--selftest',action='store_true');p.add_argument('--manifest');p.add_argument('--variant',choices=['debug','release','lab']);p.add_argument('--service-xml');p.add_argument('--resolved-package')
    args=p.parse_args()
    if args.selftest:result=selftest()
    elif args.manifest:
        assert args.variant
        result=check_manifest(ET.parse(args.manifest).getroot(),args.variant)
        if args.variant!='lab':
            assert args.service_xml and args.resolved_package, 'compiled service XML and resource value required'
            check_service_xml(ET.parse(args.service_xml).getroot(),args.resolved_package)
    else:result=source_check(Path(__file__).resolve().parents[1])
    print(json.dumps(result,indent=2))
