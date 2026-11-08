/*
 * End-to-end behaviour of the Develop build flow against the offline fake
 * runtime: the real submit helper (`submitTask`) plus the same state machine
 * the fixture renders. No DOM, no network.
 */
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {submitTask} from '../src/lib/taskSubmit.ts';
import {createDevelopMock} from './developMock.ts';

const body=(id,extra={})=>({content:'创建一个打卡模块',session_id:'sess-develop',bot_id:'develop',mode:'direct',approval_mode:'default',client_request_id:id,...extra});

test('a stored task with a lost response is resumed, not duplicated',async()=>{
  const mock=createDevelopMock();
  mock.loseNextResponse();
  const result=await submitTask(body('req-1'),mock.createTask);
  assert.equal(result.ok,true);
  assert.equal(result.attempts,2,'exactly one retry');
  assert.equal(result.task.id,'task-build-req-1');
  // The runtime stored one task and returned that same task on the retry.
  assert.equal(mock.state.calls.filter(c=>c.includes('已保存')).length,1);
  assert.equal(mock.state.calls.filter(c=>c.includes('去重')).length,1);
  assert.equal(mock.state.rows.filter(r=>r.role==='user').length,1,'the goal is recorded once, not twice');
});

test('a rejected submission is never replayed',async()=>{
  let calls=0;
  const result=await submitTask(body('req-2'),async()=>{calls++;throw Object.assign(new Error('任务目标不能为空'),{status:400});});
  assert.equal(result.ok,false);
  assert.equal(result.uncertain,false);
  assert.equal(calls,1);
});

test('a waiting_user card can be answered and parks the task on approval',async()=>{
  const mock=createDevelopMock();
  const created=await submitTask(body('req-3'),mock.createTask);
  assert.equal(created.ok,true);
  const id=created.task.id;
  // Poll: queued -> running -> waiting_user.
  await mock.task(id);
  const waiting=await mock.task(id);
  assert.equal(waiting.status,'waiting_user');
  assert.equal(waiting.pending.kind,'user');
  // Answering the card is allowed while the task is active: it is the state
  // that needs the answer.
  const answered=await submitTask(body('req-4',{content:'',choice_answer:{message_id:'msg-choice',values:['keep']}}),mock.createTask);
  assert.equal(answered.ok,true);
  assert.equal(answered.task.status,'waiting_approval');
  assert.equal(answered.task.pending.kind,'approval');
});

test('a pending approval parks the task until the operator decides',async()=>{
  const mock=createDevelopMock();
  const created=await submitTask(body('req-5'),mock.createTask);
  const id=created.task.id;
  await mock.task(id);
  await mock.task(id);
  const answered=await submitTask(body('req-6',{content:'',choice_answer:{message_id:'msg-choice',values:['keep']}}),mock.createTask);
  const approvalID=answered.task.pending.approval_id;

  // No decision yet: repeated polls must not advance or invent progress.
  for(let i=0;i<4;i++){
    const parked=await mock.task(id);
    assert.equal(parked.status,'waiting_approval');
    assert.equal(parked.total_steps,3);
  }

  // Explicit allow continues the run.
  await mock.decideTaskApproval(id,approvalID,true);
  const resumed=await mock.task(id);
  assert.equal(resumed.status,'running');
  assert.equal(resumed.pending,undefined);
  assert.equal(mock.state.calls.some(c=>c.includes('allow=true')),true);
});

test('a denied approval ends the run truthfully instead of completing',async()=>{
  const mock=createDevelopMock();
  const created=await submitTask(body('req-7'),mock.createTask);
  const id=created.task.id;
  await mock.task(id);
  await mock.task(id);
  const answered=await submitTask(body('req-8',{content:'',choice_answer:{message_id:'msg-choice',values:['keep']}}),mock.createTask);
  await mock.decideTaskApproval(id,answered.task.pending.approval_id,false);
  const after=await mock.task(id);
  assert.equal(after.status,'cancelled');
  assert.equal(after.result,undefined,'a denied run must not report a generated module');
  assert.match(after.last_error,/拒绝/);
});

test('a reconnect finds the same stored task by session',async()=>{
  const mock=createDevelopMock();
  const created=await submitTask(body('req-9'),mock.createTask);
  // A fresh page only knows the session id; the task list restores it.
  const items=await mock.tasks();
  assert.equal(items.length,1);
  assert.equal(items[0].id,created.task.id);
  assert.equal(items[0].status,'queued');
  const rows=await mock.messages();
  assert.equal(rows[0].content,'创建一个打卡模块','the transcript survives the refresh');
});

test('the module list is readable before any build, like the page mount',async()=>{
  // Develop fetches /api/dev/modules on mount; a fixture that cannot answer it
  // shows "请求失败" on an otherwise empty page.
  const mock=createDevelopMock();
  assert.deepEqual(await mock.devModules(),[]);
  assert.deepEqual(await mock.devModuleData('missing',{action:'get'}),{state:{},records:[]});
});

test('a completed build persists the module the result claims',async()=>{
  const mock=createDevelopMock();
  const created=await submitTask(body('req-10'),mock.createTask);
  const id=created.task.id;
  await mock.task(id);
  await mock.task(id);
  await submitTask(body('req-11',{content:'',choice_answer:{message_id:'msg-choice',values:['keep']}}),mock.createTask);
  await mock.decideTaskApproval(id,'apr-dev',true);
  let task=await mock.task(id);
  for(let i=0;i<6&&task.status!=='completed';i++) task=await mock.task(id);
  assert.equal(task.status,'completed');
  assert.match(task.result,/已生成「打卡」模块/);

  // The result text is only truthful if the module actually exists afterwards.
  const modules=await mock.devModules();
  assert.equal(modules.length,1,'the generated module is in the list');
  assert.equal(modules[0].name,'打卡');
  assert.equal(modules[0].enabled,true);
  assert.ok(modules[0].html.length>0,'the module has source');
  const data=await mock.devModuleData(modules[0].id,{action:'get'});
  assert.deepEqual(data.records,[]);
});

test('a terminal task does not keep claiming it is still writing',async()=>{
  const mock=createDevelopMock();
  const created=await submitTask(body('req-12'),mock.createTask);
  const id=created.task.id;
  await mock.task(id);
  await mock.task(id);
  await submitTask(body('req-13',{content:'',choice_answer:{message_id:'msg-choice',values:['keep']}}),mock.createTask);
  await mock.decideTaskApproval(id,'apr-dev',true);
  let task=await mock.task(id);
  assert.equal(task.next,'已获批准，正在写入源码');
  for(let i=0;i<6&&task.status!=='completed';i++) task=await mock.task(id);
  assert.equal(task.status,'completed');
  assert.doesNotMatch(task.next||'',/正在写入源码/,'a finished task must not show in-progress copy');
  assert.match(task.next,/完成/);
});
