#!/usr/bin/env node
import assert from 'node:assert/strict';
import { chromium } from 'playwright';
const base=new URL(process.env.RETENTION_PROBE_URL);
assert(['localhost','127.0.0.1'].includes(base.hostname));
const browser=await chromium.launch({headless:true});
const context=await browser.newContext({permissions:['clipboard-read','clipboard-write']});
const page=await context.newPage();
page.setDefaultTimeout(60000);
await page.addLocatorHandler(page.locator('.first-run-wizard'), async dialog=>dialog.getByRole('button',{name:'Close',exact:true}).click());
const errors=[],media=[];
page.on('pageerror',e=>errors.push(e.message));
page.on('request',r=>{if(r.resourceType()==='media' && !r.url().includes('/apps/firstrunwizard/'))media.push(r.url())});
try {
 await page.goto(new URL('/login',base).href);
 await page.locator('input[name="user"]').fill('admin');
 await page.locator('input[name="password"]').fill('admin');
 await page.locator('button[type="submit"]').first().click();
 await page.waitForURL(url=>!url.pathname.includes('/login'));
 await page.goto(new URL('/index.php/apps/app_api/embedded/gocassini/viewer#meeting=retention-browser',base).href);
 await page.locator('.cassini-shell').waitFor();
 await page.getByRole('button',{name:'Play unavailable: audio removed'}).waitFor();
 assert(await page.getByRole('button',{name:'Play unavailable: audio removed'}).isDisabled());
 assert.equal(await page.locator('audio').count(),0);
 await page.getByRole('button',{name:'Copy transcript',exact:true}).click();
 assert((await page.evaluate(()=>navigator.clipboard.readText())).includes('Hello'));
 await page.getByLabel('Find in this meeting',{exact:true}).fill('world');
 await page.getByRole('button',{name:'Next match',exact:true}).click();
 await page.getByLabel('Find in this meeting',{exact:true}).fill('');
 await page.getByRole('group',{name:'Choose transcript',exact:true}).getByRole('button').nth(1).click();
 await page.getByText('Hola',{exact:true}).waitFor();
 await page.getByText('Hola',{exact:true}).click();
 await page.keyboard.press('Space');
 assert.equal(await page.locator('audio').count(),0);
 assert.deepEqual(media,[]);
 assert.deepEqual(errors,[]);
 console.log('Installed retained viewer passed: login, AppAPI/CSP, text, copy, search, switch, navigation and disabled playback.');
} catch(error) {
 console.error('Installed shell:',await page.locator('.cassini-shell').innerText().catch(()=>''));
 console.error('Page errors:',errors);
 await page.screenshot({path:'/workspace/harness/runtime/d803-browser-failed.png',fullPage:true}).catch(()=>{});
 throw error;
} finally {await browser.close()}
