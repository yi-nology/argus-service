// html_script.go —— 内嵌 JS 引导与过滤：缩放拖拽 / 类型 chips / 搜索与邻域聚焦。

package impactview

// tplScriptBoot 主脚本引导：数据解析、元素引用、缩放/平移/拖拽。
func tplScriptBoot() string {
	return `
<script>
const data=JSON.parse(document.getElementById('impact-data').textContent);
const byId=Object.fromEntries(data.nodes.map(n=>[n.id,n]));
const fdata=(()=>{const el=document.getElementById('impact-findings');try{return el?JSON.parse(el.textContent):[];}catch(e){return [];}})();
const hdata=(()=>{const el=document.getElementById('impact-history');try{return el?JSON.parse(el.textContent):{};}catch(e){return {};}})();
const folddata=(()=>{const el=document.getElementById('impact-folded-syms');try{return el?JSON.parse(el.textContent):{};}catch(e){return {};}})();
const svg=document.getElementById('g'),vp=document.getElementById('viewport'),q=document.getElementById('q'),panel=document.getElementById('panel');
let scale=1,tx=0,ty=0;
function apply(){svg.style.transformOrigin='0 0';svg.style.transform='translate('+tx+'px,'+ty+'px) scale('+scale+')';}
vp.addEventListener('wheel',e=>{e.preventDefault();const f=e.deltaY<0?1.15:1/1.15;const r=vp.getBoundingClientRect();const mx=e.clientX-r.left,my=e.clientY-r.top;tx=mx-(mx-tx)*f;ty=my-(my-ty)*f;scale=Math.min(4,Math.max(.2,scale*f));apply();},{passive:false});
let drag=null;
vp.addEventListener('mousedown',e=>{drag={x:e.clientX-tx,y:e.clientY-ty};vp.classList.add('dragging');});
window.addEventListener('mousemove',e=>{if(!drag)return;tx=e.clientX-drag.x;ty=e.clientY-drag.y;apply();});
window.addEventListener('mouseup',()=>{drag=null;vp.classList.remove('dragging');});
document.getElementById('zin').onclick=()=>{scale=Math.min(4,scale*1.25);apply();};
document.getElementById('zout').onclick=()=>{scale=Math.max(.2,scale/1.25);apply();};
document.getElementById('fit').onclick=()=>{scale=1;tx=0;ty=0;q.value='';setFocus(null);filter('');panel.style.display='none';apply();};
q.addEventListener('input',()=>filter(q.value.trim().toLowerCase()));
`
}

// tplScriptChips 类型/边类型/仅改动过滤 chips 构建。
func tplScriptChips() string {
	return `// v5.6.0 探索器增强 ①类型过滤 chips（file/symbol/table/package/test 计数）
const kindFilter=new Set();
const kindsEl=document.getElementById('kinds');
const kindCounts={};data.nodes.forEach(n=>{kindCounts[n.kind]=(kindCounts[n.kind]||0)+1;});
Object.keys(kindCounts).sort().forEach(k=>{
 const b=document.createElement('span');b.className='kchip on';b.dataset.k=k;
 b.textContent=k+' '+kindCounts[k];
 b.onclick=()=>{if(kindFilter.has(k)){kindFilter.delete(k);b.classList.remove('on');}else{kindFilter.add(k);b.classList.add('on');}filter(q.value.trim().toLowerCase());};
 kindsEl.appendChild(b);
});
// v6.10.5 方向 AO：边类型过滤——按 rel 类型显隐边
(function(){
 const ek=document.getElementById('edgekinds');
 const relCounts={};
 data.edges.forEach(e=>{relCounts[e.rel]=(relCounts[e.rel]||0)+1;});
 Object.keys(relCounts).sort().forEach(r=>{
  const b=document.createElement('span');b.className='ekchip on';b.dataset.r=r;
  b.textContent=r+' '+relCounts[r];
  b.onclick=()=>{
   const on=b.classList.toggle('on');
   document.querySelectorAll('#g .edge').forEach(p=>{
    const e=data.edges[+p.dataset.i];
    if(e&&e.rel===r){p.classList.toggle('hidden',!on);}
   });
  };
  ek.appendChild(b);
 });
})();
// v6.9.6 方向 W：「只看改动」过滤 chip
(function(){
 const b=document.createElement('span');b.className='kchip';b.dataset.k='__changed';
 b.textContent='只看改动';
 let onlyChanged=false;
 b.onclick=()=>{
  onlyChanged=!onlyChanged;
  b.classList.toggle('on',onlyChanged);
  document.querySelectorAll('#g .node').forEach(g=>{
   const n=byId[g.dataset.id];
   if(onlyChanged&&n&&!n.changed){g.classList.add('dim');}
   else if(!onlyChanged){g.classList.remove('dim');}
  });
 };
 kindsEl.appendChild(b);
})();
`
}

// tplScriptFilter 搜索过滤、邻域聚焦与命中定位。
func tplScriptFilter() string {
	return `let focusId=null,focusSet=null;
function neighborhood(id){
 const s=new Set([id]);
 data.edges.forEach(e=>{if(e.from===id)s.add(e.to);if(e.to===id)s.add(e.from);});
 return s;
}
function setFocus(id){focusId=id;focusSet=id?neighborhood(id):null;}
function filter(kw){
 const hit={};
 document.querySelectorAll('#g .node').forEach(g=>{
  const id=g.dataset.id,n=byId[id];
  const kwOK=!kw||(id.toLowerCase().includes(kw)||(n&&(n.label||'').toLowerCase().includes(kw))||(n&&(n.detail||'').toLowerCase().includes(kw)));
  const kindOK=!kindFilter.size||!n||!kindFilter.has(n.kind);
  const focusOK=!focusId||id===focusId||focusSet.has(id);
  hit[id]=kwOK&&kindOK&&focusOK;
  g.classList.toggle('dim',!hit[id]);
  g.classList.toggle('focus',focusId===id);
 });
 // 边联动：kw 为空全亮；有 kw 时仅两端都命中的边保持可见
 //（data-i 回查快照边序，不依赖 DOM 序=数据序）
 document.querySelectorAll('#g .edge').forEach(p=>{
  if(!kw&&!kindFilter.size&&!focusId){p.classList.remove('dim');return;}
  const e=data.edges[+p.dataset.i];
  p.classList.toggle('dim',!(e&&hit[e.from]&&hit[e.to]));
 });
 // v6.9.3 方向 D：搜索命中自动定位——第一个命中节点居中视口
 if(kw){
  let firstHit=null;
  document.querySelectorAll('#g .node:not(.dim)').forEach(g=>{if(!firstHit)firstHit=g;});
  if(firstHit){
   const vb=svg.viewBox.baseVal;
   const rect=firstHit.getBoundingClientRect();
   const svgRect=svg.getBoundingClientRect();
   const nodeX=(rect.left+rect.width/2-svgRect.left)/scale;
   const nodeY=(rect.top+rect.height/2-svgRect.top)/scale;
   tx=vb.width/2-nodeX; ty=vb.height/2-nodeY;
   apply();
  }
 }
}
svg.addEventListener('click',e=>{
 const g=e.target.closest('.node');if(!g)return;
 const n=byId[g.dataset.id];if(!n)return;
 showPanel(n);
});
// v5.7.3 关联联动：函数级 diff 卡片/共变面板 → 聚焦对应文件节点并开侧栏
window.focusFileNode=function(fileId){
 const n=byId[fileId];if(!n)return;
 setFocus(fileId);filter(q.value.trim().toLowerCase());
 showPanel(byId[fileId]);
 document.getElementById('viewport').scrollIntoView({behavior:'smooth',block:'center'});
};
`
}
