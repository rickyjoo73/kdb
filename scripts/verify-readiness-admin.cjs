// Read a single JSON credential line from stdin. Never persist or print it.
// Use the real login endpoint; do not manufacture sessions or alter accounts.
const readline = require('readline');
if (typeof fetch !== 'function') { console.error('Node 18 or newer required'); process.exit(1); }
const input = readline.createInterface({ input: process.stdin, terminal: false });
let received = false;
input.once('close', () => { if (!received) { console.error('No credential input; verification did not run'); process.exitCode=1; } });
input.once('line', async line => {
  received = true;
  input.close(); process.stdin.destroy();
  try {
    const credentials = JSON.parse(line);
    const origin = 'https://kdb.aiinplanet.com';
    const response = await fetch(origin + '/admin/login', {
      method: 'POST', redirect: 'manual',
      body: new URLSearchParams({email: credentials.email, password: credentials.password, next: '/admin/preparations'})
    });
    if (response.status !== 302) throw new Error('Normal login failed: HTTP ' + response.status + '; no retry');
    const cookie = (response.headers.get('set-cookie') || '').split(';')[0];
    if (!cookie) throw new Error('No session returned by normal login');
    const paths=['/admin/', '/admin/workflow', '/admin/preparations'];
    if (process.env.KDB_VERIFY_COMMON === '1') paths.push('/admin/kentity','/admin/kentity/mappings');
    if (process.env.KDB_VERIFY_TDB_SHADOW === '1') paths.push('/admin/kentity/tdb');
    for (const path of paths) {
      const r = await fetch(origin + path, {headers: {Cookie: cookie}, redirect: 'manual'});
      const body = await r.text();
      if (r.status !== 200 || !body.includes('</html>') || /원장 조회 실패|집계 실패|공통 Entity 조회 실패|연결 검수 조회 실패/.test(body)) throw new Error('Admin page validation failed: ' + path + ' HTTP ' + r.status);
      console.log(JSON.stringify({path, status: r.status, complete_html: true, load_error: false}));
      if(path==='/admin/kentity/tdb' && process.env.KDB_VERIFY_TDB_MAPPING==='1'){
        const match=body.match(/href="(\/admin\/kentity\/tdb\/[a-f0-9-]{36})"/);
        if(!match)throw new Error('No TDB mapping detail link');
        const detail=await fetch(origin+match[1],{headers:{Cookie:cookie},redirect:'manual'});const html=await detail.text();
        if(detail.status!==200||!html.includes('</html>')||html.includes('상세 조회 실패')||!html.includes('언어 표기 승인은 별개'))throw new Error('TDB mapping detail validation failed');
        console.log(JSON.stringify({path:'/admin/kentity/tdb/{id}',status:detail.status,complete_html:true,load_error:false}));
      }
      if (path === '/admin/kentity' || path === '/admin/kentity/mappings') {
        const pattern=path.endsWith('/mappings') ? /href="(\/admin\/kentity\/mappings\/[a-f0-9-]{36})"/ : /href="(\/admin\/kentity\/[a-f0-9-]{36})"/;
        const match=body.match(pattern);
        if (!match) throw new Error('No common detail link: '+path);
        const detail=await fetch(origin+match[1],{headers:{Cookie:cookie},redirect:'manual'});
        const html=await detail.text();
        if (detail.status!==200 || !html.includes('</html>') || /공통 Entity 조회 실패|연결 검수 조회 실패/.test(html)) throw new Error('Common detail validation failed: '+path);
        console.log(JSON.stringify({path:path+'/{id}',status:detail.status,complete_html:true,load_error:false}));
      }
      if (path === '/admin/preparations') {
        const match = body.match(/href="(\/admin\/preparations\/[a-f0-9-]{36})"/);
        if (!match) throw new Error('No tracked request detail link');
        const detail = await fetch(origin + match[1], {headers: {Cookie: cookie}, redirect: 'manual'});
        const html = await detail.text();
        if (detail.status !== 200 || !html.includes('요청 언어별 준비 상세') || html.includes('원장 조회 실패')) throw new Error('Preparation detail validation failed');
        console.log(JSON.stringify({path:'/admin/preparations/{id}',status:detail.status,complete_html:html.includes('</html>')}));
      }
    }
  } catch (error) { console.error(error.message); process.exitCode = 1; }
});
