import {test} from 'node:test';
import assert from 'node:assert/strict';
import {lifecycleFlag,retryableSubmitError,startTaskPoll,submitTask,taskKeyOf,taskStatusText} from '../src/lib/taskSubmit.ts';
import {approvalVerdict} from '../src/lib/approvalVerdict.ts';

const task=(id,status,extra={})=>({id,bot_id:'develop',session_id:'s1',goal:'g',plan:'',progress:'',next:'',status,total_steps:0,max_steps:0,created_at:'2026-01-01T00:00:00Z',updated_at:'2026-01-01T00:00:00Z',result:'',last_error:'',approvals:[],...extra});
const fail=(status)=>{const e=new Error(status?'请求失败':'网络中断');if(status)e.status=status;return e;};

test('a lost response is retried once with the identical body and request id',async()=>{
  const calls=[];
  const accepted=task('task-1','queued');
  const result=await submitTask({content:'建一个打卡模块',session_id:'s1',client_request_id:'req-abc'},async body=>{
    calls.push(body);
    if(calls.length===1)throw fail(undefined);
    return accepted;
  });
  assert.equal(result.ok,true);
  assert.equal(result.task,accepted);
  assert.equal(result.attempts,2);
  assert.equal(calls.length,2);
  assert.deepEqual(calls[0],{content:'建一个打卡模块',session_id:'s1',client_request_id:'req-abc'});
  assert.equal(calls[0],calls[1],'the same object is resent, so the id cannot drift');
});

test('a server error is retried, a rejected request is not',async()=>{
  let n=0;
  const got=await submitTask({content:'x',client_request_id:'req-1'},async()=>{n++;throw fail(503);});
  assert.equal(got.ok,false);
  assert.equal(got.uncertain,true);
  assert.equal(n,2,'5xx is ambiguous, so one retry is attempted');
  n=0;
  const rejected=await submitTask({content:'x',client_request_id:'req-2'},async()=>{n++;throw fail(409);});
  assert.equal(rejected.ok,false);
  assert.equal(rejected.uncertain,false);
  assert.equal(n,1,'409 means the runtime already answered; replaying it is pointless');
});

test('retry never runs more than twice and surfaces an unconfirmed result',async()=>{
  let n=0;
  const got=await submitTask({content:'x',client_request_id:'req-3'},async()=>{n++;throw fail(undefined);});
  assert.equal(got.ok,false);
  assert.equal(got.uncertain,true);
  assert.equal(n,2);
});

test('a first-attempt success is never retried',async()=>{
  let n=0;
  const ok=task('task-ok','queued');
  const got=await submitTask({content:'x',client_request_id:'req-4'},async()=>{n++;return ok;});
  assert.equal(got.ok,true);
  assert.equal(got.attempts,1);
  assert.equal(n,1,'a confirmed acceptance must not be posted again');
});

test('lifecycle flag is true again after a StrictMode remount',()=>{
  const flag=lifecycleFlag();
  flag.enter();assert.equal(flag.current,true);
  flag.exit();assert.equal(flag.current,false,'cleanup drops late writes while unmounted');
  flag.enter();assert.equal(flag.current,true,'the remount must accept submissions again');
});

test('approval history never shows a pending ask as a denial',()=>{
  assert.equal(approvalVerdict({allow:false,pending:true}).label,'等待你确认');
  assert.equal(approvalVerdict({allow:true}).label,'已批准');
  assert.equal(approvalVerdict({allow:false}).label,'已拒绝');
  assert.equal(approvalVerdict({decision:'ask'}).tone,'warn');
  assert.equal(approvalVerdict({decision:'deny'}).label,'已拒绝');
  assert.equal(approvalVerdict(undefined).tone,'warn');
});

test('only connection loss and 5xx count as ambiguous',()=>{
  assert.equal(retryableSubmitError(fail(undefined)),true);
  assert.equal(retryableSubmitError(fail(502)),true);
  assert.equal(retryableSubmitError(fail(400)),false);
  assert.equal(retryableSubmitError(fail(409)),false);
  assert.equal(retryableSubmitError(new Error('boom')),true);
});

test('a poll that resolves after the loop stopped is dropped by isCurrent',async()=>{
  let release;
  const gate=new Promise(r=>{release=r;});
  const applied=[];
  const timers={setInterval:()=>1,clearInterval:()=>{}};
  const handle=startTaskPoll(10,async isCurrent=>{
    await gate;
    if(!isCurrent())return;
    applied.push('stale-write');
  },timers);
  handle.stop();
  release();
  await gate;
  await Promise.resolve();
  await new Promise(r=>setTimeout(r,0));
  assert.deepEqual(applied,[],'an unmounted poll must not write its late response');
});

test('poll ticks never overlap',async()=>{
  let running=0,peak=0,ticks=0;
  let fire;
  const timers={setInterval:fn=>{fire=fn;return 1;},clearInterval:()=>{}};
  const handle=startTaskPoll(10,async()=>{
    running++;ticks++;peak=Math.max(peak,running);
    await new Promise(r=>setTimeout(r,5));
    running--;
  },timers);
  fire();fire();
  await new Promise(r=>setTimeout(r,30));
  handle.stop();
  assert.equal(peak,1,'a slow tick must block the next one instead of overlapping');
});

test('task keys and status text never invent progress',()=>{
  // The key is the serialized task list: an activity update (not just
  // status/updated_at) changes the key, so the transcript reloads.
  const key=taskKeyOf([task('a','running',{updated_at:'1'})]);
  assert.equal(key,JSON.stringify([task('a','running',{updated_at:'1'})]));
  assert.notEqual(taskKeyOf([task('a','running',{updated_at:'1',activities:[{id:'x',name:'act',status:'running'}]})]),key);
  assert.match(taskStatusText({status:'queued'}),/排队/);
  assert.match(taskStatusText({status:'completed'}),/运行时确认/);
  assert.equal(taskStatusText({status:'failed'}),'任务失败');
});
