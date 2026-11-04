import {test} from 'node:test';
import assert from 'node:assert/strict';
import {monthlyTotal,trafficParts} from '../src/lib/subscriptions.ts';

test('mixed monthly fees convert via common USD rates and round once',()=>{
 const rates={base:'USD',rates:{USD:1,CNY:7,CAD:1.4,EUR:0.9}};
 assert.equal(monthlyTotal({USD:1000,CNY:700,CAD:140},'USD',rates).cents,1200);
 assert.equal(monthlyTotal({USD:1000,CNY:700,CAD:140},'EUR',rates).cents,1080);
 assert.equal(monthlyTotal({USD:1,CNY:1},'USD',rates).cents,1);
});
test('unavailable and unsupported currencies stay explicit',()=>{
 assert.deepEqual(monthlyTotal({USD:100,CNY:700},'USD').missing,{CNY:700});
 assert.deepEqual(monthlyTotal({USD:100,USDT:500},'USD',{rates:{USD:1}}).missing,{USDT:500});
 assert.equal(monthlyTotal({USD:100},'USD').cents,100);
 assert.equal(monthlyTotal({},'USD').cents,0);
});
test('traffic uses out on the left and in on the right without quota',()=>{
 assert.deepEqual(trafficParts(300,100),{total:400,outPercent:75,inPercent:25});
 assert.deepEqual(trafficParts(0,0),{total:0,outPercent:0,inPercent:0});
 assert.equal(trafficParts(0,100).inPercent,100);
 assert.equal(trafficParts(100,0).outPercent,100);
});
