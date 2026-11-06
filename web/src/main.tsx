import React from 'react'
import ReactDOM from 'react-dom/client'
import { BrowserRouter, NavLink } from 'react-router-dom'
import './style.css'

function App() {
  return <div className="shell"><aside><strong>TunnelQuay</strong><nav>{['隧道','设备','连接','设置'].map((name,i)=><NavLink key={name} to={['/','/devices','/connections','/settings'][i]}>{name}</NavLink>)}</nav></aside><main><header><h1>隧道</h1></header><div className="empty">管理 API 尚未接入</div></main></div>
}

ReactDOM.createRoot(document.getElementById('root')!).render(<React.StrictMode><BrowserRouter><App/></BrowserRouter></React.StrictMode>)
