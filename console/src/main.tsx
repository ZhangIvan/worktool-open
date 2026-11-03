import React from 'react';
import { createRoot } from 'react-dom/client';
import App from './App';
import GoConsole from './GoConsole';
import { isGoMode } from '../mode';
import './style.css';
const Console = isGoMode(import.meta.env.MODE) ? GoConsole : App;
createRoot(document.getElementById('root')!).render(<React.StrictMode><Console/></React.StrictMode>);
