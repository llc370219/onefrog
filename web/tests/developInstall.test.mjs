/*
 * Regression for the fixture's install lifecycle. The Develop page fetches its
 * module list on mount, and the preview is rendered inside <StrictMode>, which
 * runs effects as setup → cleanup → setup. A mock owned by an effect is
 * therefore uninstalled between the two mounts and the second mount reaches the
 * real backend ("请求失败"). This test reproduces that exact order.
 */
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {createDevelopMock, installDevelopMock} from './developMock.ts';

/** Stands in for the real `api` object: every call fails like an unreachable backend. */
function realApi(){
  const real=()=>async()=>{throw new Error('请求失败');};
  return {
    devModules:real(),createDevModule:real(),updateDevModule:real(),deleteDevModule:real(),
    enableModule:real(),devModuleData:real(),tasks:real(),task:real(),messages:real(),
    createTask:real(),taskAction:real(),guideTask:real(),decideTaskApproval:real(),
  };
}

/** What the page does on mount: read the module list and the frame init data. */
async function mountFetch(api){
  const rows=await api.devModules();
  const data=await api.devModuleData('mod-checkin',{action:'get'});
  return {rows,data};
}

test('an effect-owned mock fails the StrictMode replay, which is the reported bug',async()=>{
  const api=realApi();
  const mock=createDevelopMock();
  // Effect setup: install. First mount fetch sees the fake.
  const restore=installDevelopMock(api,mock);
  assert.deepEqual((await mountFetch(api)).rows,[]);
  // StrictMode cleanup restores the real api; the replay's setup is guarded by
  // an "already installed" ref and does not install again.
  restore();
  await assert.rejects(mountFetch(api),/请求失败/,'second mount reaches the real backend: the reported 请求失败');
});

test('a harness-owned mock survives the whole StrictMode replay',async()=>{
  const api=realApi();
  const mock=createDevelopMock();
  // Installed once before render, torn down only when the fixture really goes
  // away — so both mounts see the fake.
  const restore=installDevelopMock(api,mock);
  try{
    const first=await mountFetch(api);
    assert.deepEqual(first.rows,[],'the empty page loads a real list, not an error');
    assert.deepEqual(first.data,{state:{},records:[]});
    // StrictMode replay: the second mount runs the same fetch again.
    const second=await mountFetch(api);
    assert.deepEqual(second.rows,[]);
    assert.deepEqual(second.data,{state:{},records:[]});
  }finally{
    restore();
  }
  await assert.rejects(mountFetch(api),/请求失败/,'teardown restores the real api');
});

test('re-installing the mock is idempotent and restore is safe to call once',async()=>{
  const api=realApi();
  const mock=createDevelopMock();
  const restoreA=installDevelopMock(api,mock);
  const restoreB=installDevelopMock(api,mock);
  assert.deepEqual(await api.devModules(),[]);
  restoreB();
  assert.deepEqual(await api.devModules(),[],'a second install must not leave the real api behind');
  restoreA();
  await assert.rejects(api.devModules(),/请求失败/);
});

test('the fixture log receives calls from the installed fake',async()=>{
  const api=realApi();
  const mock=createDevelopMock();
  const restore=installDevelopMock(api,mock);
  const seen=[];
  const off=mock.subscribe(line=>seen.push(line));
  await mock.createDevModule({name:'打卡',description:'夹具'});
  assert.equal(seen.length,1);
  assert.match(seen[0],/POST \/api\/dev\/modules/);
  off();
  await mock.createDevModule({name:'待办'});
  assert.equal(seen.length,1,'unsubscribe stops the log');
  restore();
});
