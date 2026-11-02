from pathlib import Path
import json
from playwright.sync_api import sync_playwright
root=Path(__file__).resolve().parents[1];out=root/'artifacts/ui';out.mkdir(parents=True,exist_ok=True)
creds=json.loads((root/'.runtime/ui-credentials.json').read_text());user=next(x for x in creds['users'] if x['role']=='operator')
checks=[]
with sync_playwright() as p:
 browser=p.chromium.launch(executable_path='/usr/bin/chromium',headless=True,args=['--no-sandbox','--disable-dev-shm-usage'])
 context=browser.new_context(viewport={'width':1440,'height':1000})
 context.route('**/*',lambda route: route.continue_() if route.request.url.startswith(('http://127.0.0.1:5173/','http://127.0.0.1:3100/','ws://127.0.0.1:5173/','data:')) else route.abort())
 page=context.new_page();errors=[];page.on('pageerror',lambda e:errors.append(str(e)))
 page.goto('http://127.0.0.1:5173/');page.get_by_role('button',name='登录',exact=True).wait_for();page.screenshot(path=str(out/'01-login.png'),full_page=True)
 page.get_by_label('工作区',exact=True).fill(creds['tenantId']);page.get_by_label('账号',exact=True).fill(user['loginName']);page.get_by_label('密码',exact=True).fill(user['password']);page.get_by_role('button',name='登录',exact=True).click()
 page.get_by_role('heading',name='运行概览',exact=True).wait_for();checks.append({'name':'real login and overview','pass':True});page.screenshot(path=str(out/'02-overview.png'),full_page=True)
 page.get_by_role('link',name='67 项能力目录',exact=True).click();page.get_by_role('heading',name='67 项能力目录',exact=True).wait_for();checks.append({'name':'67 capability rows','pass':page.locator('tbody tr').count()==67});page.screenshot(path=str(out/'03-capabilities.png'),full_page=False)
 page.get_by_role('link',name='任务',exact=True).click();page.wait_for_timeout(200);page.screenshot(path=str(out/'04-tasks-empty.png'),full_page=True)
 (out/'visible-text.txt').write_text(page.locator('body').inner_text())
 checks.append({'name':'no browser runtime errors','pass':not errors,'errors':errors});(out/'results.json').write_text(json.dumps({'checks':checks,'android':'NOT_RUN','scope':'real browser against loopback backend synthetic identities'},indent=2))
 browser.close()
print(json.dumps(checks))
