import {test} from 'node:test';
import assert from 'node:assert/strict';
import {counts,glanceItems,intervalLabel,isWatched,sameSelection,targetOf,toggle,watchKey} from '../src/lib/serviceWatch.ts';

test('ports are watched by listen address, others by name',()=>{
 assert.deepEqual(targetOf({kind:'ports',name:'nginx',ports:'0.0.0.0:80'}),{kind:'ports',name:'0.0.0.0:80'});
 assert.deepEqual(targetOf({kind:'docker',name:'web',ports:'0.0.0.0:8080->80/tcp'}),{kind:'docker',name:'web'});
 assert.equal(targetOf({kind:'ports',name:'nginx'}),null);
 assert.notEqual(watchKey({kind:'docker',name:'a b'}),watchKey({kind:'docker a',name:'b'}));
});
test('toggle adds, removes and drops empty servers',()=>{
 const web={kind:'docker',name:'web'};
 let sel=toggle({},'node',web);
 assert.ok(isWatched(sel,'node',web));
 assert.equal(isWatched(sel,'other',web),false);
 sel=toggle(sel,'node',web);
 assert.deepEqual(sel,{});
});
test('selection comparison ignores order',()=>{
 const a={kind:'docker',name:'a'},b={kind:'systemd',name:'b.service'};
 assert.ok(sameSelection({n:[a,b]},{n:[b,a]}));
 assert.equal(sameSelection({n:[a]},{n:[a,b]}),false);
 assert.equal(sameSelection({n:[a]},{m:[a]}),false);
});
test('counts split healthy, failing and undecided items',()=>{
 const s=[{server_id:'n',state:'ok'},{server_id:'n',state:'fault'},{server_id:'n',state:'missing'},{server_id:'m',state:'unknown'},{server_id:'m',state:'pending'}];
 assert.deepEqual(counts(s),{total:5,ok:1,bad:2,unknown:1});
 assert.deepEqual(counts(s,'m'),{total:2,ok:0,bad:0,unknown:1});
 assert.equal(intervalLabel(30),'30 秒');
 assert.equal(intervalLabel(300),'5 分钟');
 assert.equal(intervalLabel(3600),'1 小时');
});
test('dashboard glance lists only the saved selection, service name before server',()=>{
 const sel={a:[{kind:'docker',name:'web'},{kind:'systemd',name:'nginx.service'}],b:[{kind:'ports',name:'0.0.0.0:22'}],gone:[{kind:'docker',name:'old'}]};
 const status=[
  {server_id:'a',kind:'docker',name:'web',state:'fault',detail:'exited'},
  {server_id:'a',kind:'systemd',name:'nginx.service',state:'ok'},
  {server_id:'b',kind:'ports',name:'0.0.0.0:22',state:'ok'},
  {server_id:'a',kind:'docker',name:'unselected',state:'fault'},
 ];
 const rows=glanceItems(sel,status,['b','a']);
 assert.deepEqual(rows.map(r=>r.name),['web','0.0.0.0:22','nginx.service']);
 assert.equal(rows[0].state,'fault');
 assert.equal(rows[0].server_id,'a');
 assert.equal(rows.find(r=>r.name==='old'),undefined);
 const pending=glanceItems({a:[{kind:'docker',name:'new'}]},[],['a']);
 assert.equal(pending[0].state,'pending');
});
