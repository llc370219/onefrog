import {test} from 'node:test';
import assert from 'node:assert/strict';
import {groupMembers,groupSessions,isDirect,latestDirect,speakerOf} from '../src/lib/chatGroups.ts';

const s=(id,bot_id,updated_at,mode)=>({id,bot_id,title:id,preview:'',updated_at,options:mode?{mode}:undefined});
const sessions=[
 s('g1','main','2026-11-06T10:00:00Z'),
 s('d1','check','2026-11-06T09:00:00Z','direct'),
 s('inbox-check','check','2026-11-06T11:00:00Z'),
 s('d2','main','2026-11-06T08:00:00Z','direct'),
 s('g2','main','2026-11-06T12:00:00Z','team'),
];

test('direct and inbox sessions count as one-to-one chats',()=>{
 assert.equal(isDirect(sessions[0]),false);
 assert.equal(isDirect(sessions[1]),true);
 assert.equal(isDirect(sessions[2]),true);
 assert.equal(isDirect(sessions[4]),false);
 assert.equal(isDirect(s('auto-a1-check','check','2026-11-06T13:00:00Z')),true);
});

test('group chats and each bot history are kept apart, newest first',()=>{
 const {groups,byBot}=groupSessions(sessions);
 assert.deepEqual(groups.map(x=>x.id),['g2','g1']);
 assert.deepEqual(byBot.check.map(x=>x.id),['inbox-check','d1']);
 assert.deepEqual(byBot.main.map(x=>x.id),['d2']);
});

test('clicking a bot reopens its latest one-to-one chat',()=>{
 assert.equal(latestDirect(sessions,'check')?.id,'inbox-check');
 assert.equal(latestDirect(sessions,'main')?.id,'d2');
 assert.equal(latestDirect(sessions,'nobody'),undefined);
});

const bots=[{id:'main',name:'主控',enabled:true},{id:'check',name:'巡检',enabled:true},{id:'db',name:'数据库',enabled:true},{id:'off',name:'停用',enabled:false}];

test('timeline speakers map back to bots by name',()=>{
 assert.equal(speakerOf(bots,'巡检')?.id,'check');
 assert.equal(speakerOf(bots,'db')?.id,'db');
 assert.equal(speakerOf(bots,''),undefined);
});

test('group members skip excluded and disabled bots but always keep the lead first',()=>{
 assert.deepEqual(groupMembers(bots,'main',['db']).map(b=>b.id),['main','check']);
 assert.deepEqual(groupMembers(bots,'main',['main']).map(b=>b.id),['main','check','db']);
});
