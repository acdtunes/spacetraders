import { launch } from './cdp.mjs';
import { mkdirSync } from 'node:fs';
const OUT=process.env.OUT, TAG=process.env.TAG, PORT=process.env.VIZ_PORT??'5199';
mkdirSync(OUT,{recursive:true});
const b=await launch({width:1600,height:900,port:Number(process.env.CDP_PORT??9370)});
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
async function waitFresh(){for(let i=0;i<60;i++){const s=await b.evaluate(`document.querySelector('[data-testid="freshness-full-scale"]')?.textContent ?? ''`);if(s&&s!=='—')return s;await sleep(400);} throw new Error('freshness never landed');}
for (const [name,url] of [['galaxy','/trade-flows'],['region','/trade-flows?band=region'],['system','/trade-flows?band=system&focus=X1-KD64']]) {
  await b.goto(`http://localhost:${PORT}${url}`,3000);
  await waitFresh(); await sleep(3200);
  await b.shot(`${OUT}/${TAG}-${name}.png`);
  console.log('wrote',`${OUT}/${TAG}-${name}.png`);
}
b.close(); process.exit(0);
