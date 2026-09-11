import { mount } from 'svelte';
import Preview from './Preview.svelte';
import '../../../cassini-app/src/app.css';
window.__CASSINI_CONFIG__ = { operatorBasePath: 'https://cloud.example.com/index.php/apps/app_api/proxy/gocassini/operator' };
mount(Preview, { target: document.getElementById('app')! });
