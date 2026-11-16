// html_script_panel.go —— 内嵌 JS 侧栏与交互收尾：详情面板 / 卡片联动 / 目录展开 / 小地图 / 拖拽。

package impactview

// tplScriptPanel 详情侧栏：funcdiff 分支与目录/包聚合块。
func tplScriptPanel() string {
	return `function showPanel(n){
 // v5.7.3 邻域聚焦：点选节点高亮其一跳邻居，再点同节点/复位取消
 setFocus(focusId===n.id?null:n.id);
 filter(q.value.trim().toLowerCase());
 const edgesIn=data.edges.filter(x=>x.to===n.id),edgesOut=data.edges.filter(x=>x.from===n.id);
 const li=a=>a.map(x=>'<div class="kv">'+(x.rel||'')+' → '+(byId[x.to]?byId[x.to].label:x.to)+'</div>').join('')||'<div class="kv">—</div>';
 // v5.7.4 函数级变更节点：完整 before/after 签名与调用增减
 if(n.kind==='funcdiff'){
  const m=(n.id||'').match(/^fd:(.+):([^:]+)$/);
  const fd=(data.func_diffs||[]).find(f=>f.file===m[1]&&f.name===m[2]);
  if(fd){
   const stTxt={added:'🟢 新增',removed:'🔴 删除'}[fd.status]||'🟡 变更';
   const card=x=>'<pre style="background:#f1f5f9;padding:6px;border-radius:6px;font-size:12px;margin:2px 0;white-space:pre-wrap;word-break:break-all">'+esc(x||'（无）')+'</pre>';
   panel.innerHTML='<h3>'+stTxt+' '+esc(fd.name)+'()</h3>'
    +'<div class="kv"><b>文件</b>'+esc(fd.file)+'</div>'
    +'<div class="kv"><b>签名</b>'+(fd.sig_same?'未变化':'已变化')+'</div>'
    +'<h3 style="margin-top:12px">BEFORE</h3>'+card(fd.before&&fd.before.signature)
    +'<h3 style="margin-top:8px">AFTER</h3>'+card(fd.after&&fd.after.signature)
    +(((fd.calls_added||[]).length||((fd.calls_removed||[]).length))?'<h3 style="margin-top:8px">调用增减</h3>'+(fd.calls_added||[]).slice(0,6).map(c=>'<div class="kv">+ '+esc(c)+'</div>').join('')+(fd.calls_removed||[]).slice(0,6).map(c=>'<div class="kv">− '+esc(c)+'</div>').join(''):'');
   panel.style.display='block';
   document.getElementById('closepanel').style.display='inline-block';
   return;
  }
 }
 // v6.9.2 方向 C 实际落地：目录聚合节点（dir: 前缀）点击展开成员文件列表
 let dirExtra='';
 if(n.kind==='package'&&n.id.startsWith('dir:')){
  const dirPath=n.id.slice(4);
  const members=(data.nodes||[]).filter(x=>x.id.startsWith('file:')&&x.id.slice(5).startsWith(dirPath+'/'));
  if(members.length){
   dirExtra='<h3 style="margin-top:12px">目录聚合</h3><div class="kv"><b>路径</b>'+esc(dirPath)+'</div>'
    +'<div class="kv"><b>文件数</b>'+members.length+'</div>'
    +'<h3 style="margin-top:8px">成员文件</h3>'+members.map(m=>'<div class="kv">'+esc(m.label)+'</div>').join('');
  }
 }
 // v5.7.4 包聚合详情：文件数/测试覆盖/对外暴露/深度
 let pkgExtra='';
 if(n.kind==='package'&&data.packages){
  const p=(data.packages||[]).find(x=>'pkg:'+x.path===n.id);
  if(p){
   pkgExtra='<div class="kv"><b>Go 文件</b>'+p.files+'</div>'
    +'<div class="kv"><b>测试</b>'+(p.tests>0?p.tests+' 个 *_test.go':'<b style="color:#dc2626">无测试覆盖</b>')+'</div>'
    +(p.exposed?'<div class="kv"><b>暴露面</b>被受影响集之外的包依赖</div>':'')
    +'<div class="kv"><b>距改动</b>'+p.depth+' 跳'+(p.direct_change?'（含直接改动）':'')+'</div>';
  }
 }
`
}

// tplScriptPanelFile 详情侧栏续：文件级折叠函数/共变/意见块与面板装配。
func tplScriptPanelFile() string {
	return ` // 关联融合：函数级 diff（文件节点的改动函数）与历史共变（import 图外隐性耦合）
 const filePath=n.id.startsWith('file:')?n.id.slice(5):null;
 const stIcon={added:'🟢',removed:'🔴'};
 let extra='';
 if(n.kind==='package'&&dirExtra){extra=dirExtra;}
 else if(n.kind==='package'&&pkgExtra){extra='<h3 style="margin-top:12px">包聚合</h3>'+pkgExtra;}
 else if(filePath){
  // v6.9.3 方向 E：被折叠函数可点击 → 触发搜索定位（v6.9.2 展示版的交互超集）
  const folded=folddata[n.id]||[];
  if(folded.length){
   extra+='<h3 style="margin-top:12px">被折叠函数 '+folded.length+'</h3>'+folded.map(fn=>'<div class="kv cplrow" onclick="q.value=\''+esc(fn).replace(/'/g,"\\'")+';q.dispatchEvent(new Event(\'input\'))">'+esc(fn)+'</div>').join('');
  }
  const fds=(data.func_diffs||[]).filter(fd=>fd.file===filePath);
  if(fds.length){
   extra+='<h3 style="margin-top:12px">函数级变更 '+fds.length+'</h3>'+fds.map(fd=>{
    const ic=stIcon[fd.status]||'🟡';
    const ln=((fd.before&&fd.before.lines)??'—')+'→'+((fd.after&&fd.after.lines)??'—');
    return '<div class="kv">'+ic+' <b>'+esc(fd.name)+'</b>'+(fd.sig_same?'（签名未变）':'')+' 行数 '+ln+'</div>'
     +(fd.calls_added||[]).slice(0,4).map(c=>'<div class="kv">+ '+esc(c)+'</div>').join('')
     +(fd.calls_removed||[]).slice(0,4).map(c=>'<div class="kv">− '+esc(c)+'</div>').join('');
   }).join('');
  }
  const coup=(data.coupling||[]).filter(c=>c.file===filePath||c.other===filePath);
  if(coup.length){
   extra+='<h3 style="margin-top:12px">历史共变 '+coup.length+'</h3>'+coup.map(c=>{
    const o=c.other===filePath?c.file:c.other;
    return '<div class="cplrow" onclick="window.focusFileNode && window.focusFileNode(\'file:'+esc(o)+'\')">'+esc(o)+' ×'+c.count+'</div>';
   }).join('');
  }
  // v5.8.0/v5.8.2 审查意见挂图+历史档案：该文件的 findings 与跨 MR 意见史
  if(hdata[filePath]&&hdata[filePath].total){
   const h=hdata[filePath];
   extra+='<h3 style="margin-top:12px">历史档案</h3><div class="kv">该文件历史上共 <b>'+h.total+'</b> 条意见'+(h.suppressed>0?'，其中 <b>'+h.suppressed+'</b> 条被人工抑制（误报倾向参考，审阅可降权）':'')+'</div>';
  }
  if(fdata.length){
   const fs=fdata.filter(f=>f.file===filePath);
   if(fs.length){
    const sev={high:'🔴',medium:'🟡',critical:'🔴',low:'🟢'};
    extra+='<h3 style="margin-top:12px">审查意见 '+fs.length+'</h3>'+fs.map(f=>
     '<div class="kv">'+(sev[(f.severity||'').toLowerCase()]||'⚪')+' <b>'+(f.line||'?')+':</b> '+esc(f.comment)
     +(f.fp?' <span class="cplrow" title="点击复制修复指令" onclick="navigator.clipboard&&navigator.clipboard.writeText(\'fix fp:'+esc(String(f.fp).slice(0,8))+'\')">📋fp:'+esc(String(f.fp).slice(0,8))+'</span>':'')+'</div>').join('')
     +'<div class="kv" style="color:#64748b">完整意见与修复建议见任务报告</div>';
   }
  }
 }
 panel.innerHTML='<h3>'+esc(n.label)+'</h3>'
  +'<div class="kv"><b>类型</b>'+esc(n.kind)+(n.changed?'（本轮改动）':'')+'</div>'
  +(n.detail?'<div class="kv"><b>详情</b>'+esc(n.detail)+'</div>':'')
  +'<div class="kv"><b>ID</b>'+esc(n.id)+'</div>'
  // v6.9.6 方向 S：复制文件路径按钮
  +(n.id.startsWith('file:')?' <span class="cplrow" title="点击复制路径" onclick="navigator.clipboard&&navigator.clipboard.writeText(\''+esc(n.id.slice(5)).replace(/'/g,"\\'")+'\')">📋复制路径</span>':'')
  +'<div class="kv"><b>聚焦</b>'+(focusId?'已聚焦一跳邻居（再点本节点取消）':'未聚焦')+'</div>'
  +'<h3 style="margin-top:12px">入边 '+edgesIn.length+'</h3>'+li(edgesIn)
  +'<h3 style="margin-top:12px">出边 '+edgesOut.length+'</h3>'+li(edgesOut)
  +extra;
 panel.style.display='block';
 document.getElementById('closepanel').style.display='inline-block';
}
`
}

// tplScriptMisc 卡片联动、快捷键、导出 SVG、URL 直达、分享与全屏。
func tplScriptMisc() string {
	return `// 函数级 diff 卡片联动（funcDiffDetails 产物）
document.querySelectorAll('.fdcard[data-fdfile]').forEach(el=>{
 el.addEventListener('click',()=>window.focusFileNode('file:'+el.dataset.fdfile));
});
document.getElementById('closepanel').onclick=function(){panel.style.display='none';this.style.display='none';};
// v6.9.9 方向 AD：键盘快捷键——Esc 关侧栏，F 复位，+/− 缩放
document.addEventListener('keydown',e=>{
 if(e.target.tagName==='INPUT')return;
 switch(e.key){
  case 'Escape':panel.style.display='none';document.getElementById('closepanel').style.display='none';break;
  case 'f':case 'F':document.getElementById('fit').click();break;
  case '+':case '=':document.getElementById('zin').click();break;
  case '-':document.getElementById('zout').click();break;
 }
});
// v5.6.0 探索器增强 ③导出 SVG：克隆图+内联样式单文件落盘（离线评审/贴周报用）
document.getElementById('export').onclick=()=>{
 const clone=svg.cloneNode(true);
 const st=document.createElementNS('http://www.w3.org/2000/svg','style');
 st.textContent=Array.from(document.styleSheets).flatMap(ss=>{try{return Array.from(ss.cssRules).map(r=>r.cssText);}catch(e){return [];}}).join('\n');
 clone.insertBefore(st,clone.firstChild);
 clone.setAttribute('xmlns','http://www.w3.org/2000/svg');
 const blob=new Blob([new XMLSerializer().serializeToString(clone)],{type:'image/svg+xml'});
 const a=document.createElement('a');a.href=URL.createObjectURL(blob);
 a.download='argus-impact-'+(data&&data.nodes?data.nodes.length:'x')+'.svg';a.click();
 setTimeout(()=>URL.revokeObjectURL(a.href),3000);
};
function esc(s){return String(s).replace(/[&<>"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]));}
// v6.9.5 方向 R：URL 参数直达——?focus=<nodeId> 自动聚焦并开侧栏
(function(){
 const params=new URLSearchParams(location.search);
 const focus=params.get('focus');
 if(focus&&byId[focus]){focusFileNode(focus);}
 // v6.10.0 方向 AG：视图状态恢复——?scale=&tx=&ty= 精确恢复缩放/平移
 const s=parseFloat(params.get('scale')),txp=parseFloat(params.get('tx')),typ=parseFloat(params.get('ty'));
 if(!isNaN(s))scale=s; if(!isNaN(txp))tx=txp; if(!isNaN(typ))ty=typ;
})();
apply();
// v6.10.0 方向 AG：「分享视图」按钮——复制当前 focus/scale/tx/ty 到剪贴板
document.getElementById('shareview')?.addEventListener('click',()=>{
 const u=new URL(location.href);
 if(focusId)u.searchParams.set('focus',focusId);else u.searchParams.delete('focus');
 u.searchParams.set('scale',scale.toFixed(2));
 u.searchParams.set('tx',Math.round(tx));
 u.searchParams.set('ty',Math.round(ty));
 navigator.clipboard&&navigator.clipboard.writeText(u.toString());
});
// v6.10.4 全屏模式——document.documentElement.requestFullscreen + body.fullscreen CSS
document.getElementById('fullscreen')?.addEventListener('click',()=>{
 if(document.fullscreenElement){document.exitFullscreen();document.body.classList.remove('fullscreen');}
 else{document.documentElement.requestFullscreen();document.body.classList.add('fullscreen');}
});
`
}

// tplScriptExpand 目录双击展开与 PNG 导出。
func tplScriptExpand() string {
	return `// ========== v6.11.0 四连：目录展开 + PNG导出 + 小地图 + 节点拖拽 ==========
// v6.11.0 AC：目录节点双击展开/收起——动态插入/移除成员文件节点
const expandedDirs=new Set();
svg.addEventListener('dblclick',e=>{
 const g=e.target.closest('.node');
 if(!g)return;
 const n=byId[g.dataset.id];
 if(!n||n.kind!=='package'||!n.id.startsWith('dir:'))return;
 e.stopPropagation();
 const dirPath=n.id.slice(4);
 if(expandedDirs.has(n.id)){
  // 收起：移除成员节点和边
  expandedDirs.delete(n.id);
  svg.querySelectorAll('[data-dir="'+n.id+'"]').forEach(el=>el.remove());
  g.querySelector('rect').setAttribute('stroke-dasharray','');
  return;
 }
 expandedDirs.add(n.id);
 const members=(data.nodes||[]).filter(x=>x.id.startsWith('file:')&&x.id.slice(5).startsWith(dirPath+'/'));
 if(!members.length)return;
 const gRect=g.querySelector('rect');
 const baseX=parseFloat(g.getAttribute('transform').match(/translate\(([\d.]+)/)[1]);
 const baseY=parseFloat(g.getAttribute('transform').match(/translate\(([\d.]+),([\d.]+)\)/)[2]);
 members.forEach((m,i)=>{
  const y=baseY+42+i*36;
  const gEl=document.createElementNS('http://www.w3.org/2000/svg','g');
  gEl.setAttribute('class','node kind-file');
  gEl.setAttribute('data-id',m.id);
  gEl.setAttribute('data-dir',n.id);
  gEl.setAttribute('transform','translate('+baseX+','+y+')');
  gEl.innerHTML='<title>file · '+esc(m.id)+'</title><rect width="240" height="30" rx="6" fill="#e2e8f0" stroke="#64748b"/><text x="12" y="20">'+esc(m.label.length>28?m.label.slice(0,27)+'…':m.label)+'</text>';
  gEl.addEventListener('click',ev=>{ev.stopPropagation();showPanel(byId[m.id]);});
  svg.appendChild(gEl);
  // 画从目录节点到成员文件的虚线
  const path=document.createElementNS('http://www.w3.org/2000/svg','path');
  path.setAttribute('d','M'+(baseX+120)+','+(baseY+30)+' L'+(baseX+120)+','+y);
  path.setAttribute('class','edge');
  path.setAttribute('data-dir',n.id);
  path.setAttribute('style','stroke-dasharray:3,3;stroke-width:1');
  svg.insertBefore(path,gEl);
 });
 g.querySelector('rect').setAttribute('stroke-dasharray','6,3');
});
// v6.11.0 AK：导出 PNG——SVG → Canvas → toDataURL
document.getElementById('exportpng')?.addEventListener('click',()=>{
 const clone=svg.cloneNode(true);
 const st=document.createElementNS('http://www.w3.org/2000/svg','style');
 st.textContent=Array.from(document.styleSheets).flatMap(ss=>{try{return Array.from(ss.cssRules).map(r=>r.cssText);}catch(e){return [];}}).join('\n');
 clone.insertBefore(st,clone.firstChild);
 clone.setAttribute('xmlns','http://www.w3.org/2000/svg');
 const xml=new XMLSerializer().serializeToString(clone);
 const img=new Image();
 img.onload=()=>{
  const canvas=document.createElement('canvas');
  canvas.width=svg.viewBox.baseVal.width*2;
  canvas.height=svg.viewBox.baseVal.height*2;
  const ctx=canvas.getContext('2d');
  ctx.scale(2,2);
  ctx.fillStyle='#fff';ctx.fillRect(0,0,canvas.width,canvas.height);
  ctx.drawImage(img,0,0);
  const a=document.createElement('a');
  a.href=canvas.toDataURL('image/png');
  a.download='argus-impact-'+(data&&data.nodes?data.nodes.length:'x')+'.png';
  a.click();
 };
 img.src='data:image/svg+xml;charset=utf-8,'+encodeURIComponent(xml);
});
`
}

// tplScriptMinimap 小地图与节点拖拽，收尾 </script></body></html>。
func tplScriptMinimap() string {
	return `// v6.11.0 AM：小地图——全图缩略 + 当前视口矩形
(function(){
 const mm=document.getElementById('mm');
 if(!mm)return;
 const gRect=document.getElementById('g');
 const vb=gRect.viewBox.baseVal;
 // 简化的节点缩略点
 data.nodes.forEach(n=>{
  const gEl=gRect.querySelector('[data-id="'+CSS.escape(n.id)+'"]');
  if(!gEl)return;
  const t=gEl.getAttribute('transform');
  const m=t&&t.match(/translate\(([\d.]+),([\d.]+)\)/);
  if(!m)return;
  const r=document.createElementNS('http://www.w3.org/2000/svg','rect');
  r.setAttribute('x',parseFloat(m[1])/8);r.setAttribute('y',parseFloat(m[2])/8);
  r.setAttribute('width',30);r.setAttribute('height',4);
  r.setAttribute('fill',n.changed?'#dc2626':'#94a3b8');
  r.setAttribute('opacity',n.changed?'0.9':'0.4');
  mm.appendChild(r);
 });
 // 视口指示器
 const vp=document.createElementNS('http://www.w3.org/2000/svg','rect');
 vp.setAttribute('class','vp');
 mm.appendChild(vp);
 function updateVp(){
  const vpW=document.getElementById('viewport').clientWidth;
  const vpH=document.getElementById('viewport').clientHeight;
  const mmScale=180/vb.width;
  vp.setAttribute('x',(-tx/scale)*mmScale);
  vp.setAttribute('y',(-ty/scale)*mmScale);
  vp.setAttribute('width',(vpW/scale)*mmScale);
  vp.setAttribute('height',(vpH/scale)*mmScale);
 }
 updateVp();
 const origApply=apply;
 apply=function(){origApply();updateVp();};
})();
// v6.11.0 AN：节点拖拽——mousedown 节点上拖动更新位置
let nodeDrag=null;
svg.addEventListener('mousedown',e=>{
 const g=e.target.closest('.node');
 if(!g)return;
 e.stopPropagation();
 const id=g.dataset.id;
 const startX=e.clientX,startY=e.clientY;
 const origTx=tx,origTy=ty;
 vp.classList.add('nodedrag');
 nodeDrag={id,g,origTx,origTy,startX,startY};
 const move=ev=>{
  if(!nodeDrag)return;
  const dx=(ev.clientX-startX)/scale,dy=(ev.clientY-startY)/scale;
  const cur=g.getAttribute('transform').match(/translate\(([\d.]+),([\d.]+)\)/);
  if(cur){
   const nx=parseFloat(cur[1])+dx,ny=parseFloat(cur[2])+dy;
   g.setAttribute('transform','translate('+nx+','+ny+')');
   nodeDrag.startX=ev.clientX;nodeDrag.startY=ev.clientY;
   // 更新连接到该节点的边
   svg.querySelectorAll('.edge').forEach(p=>{
    const ei=+p.dataset.i;
    const ed=data.edges[ei];
    if(!ed)return;
    if(ed.from===id||ed.to===id){
     // 简化：只更新端点在该节点上的边的起点/终点 Y 坐标
     const fromG=svg.querySelector('[data-id="'+CSS.escape(ed.from)+'"]');
     const toG=svg.querySelector('[data-id="'+CSS.escape(ed.to)+'"]');
     if(!fromG||!toG)return;
     const ft=fromG.getAttribute('transform').match(/translate\(([\d.]+),([\d.]+)\)/);
     const tt=toG.getAttribute('transform').match(/translate\(([\d.]+),([\d.]+)\)/);
     if(!ft||!tt)return;
     const x1=parseFloat(ft[0])+240,y1=parseFloat(ft[1])+15;
     const x2=parseFloat(tt[0]),y2=parseFloat(tt[1])+15;
     const mid=(x1+x2)/2;
     p.setAttribute('d','M'+x1+','+y1+' C'+mid+','+y1+' '+mid+','+y2+' '+x2+','+y2);
    }
   });
  }
 };
 const up=()=>{
  nodeDrag=null;
  vp.classList.remove('nodedrag');
  window.removeEventListener('mousemove',move);
  window.removeEventListener('mouseup',up);
 };
 window.addEventListener('mousemove',move);
 window.addEventListener('mouseup',up);
});
</script></body></html>`
}
