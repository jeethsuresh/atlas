/* Shared, bounded, memory-only destination history. Never store credentials or actions. */
(function(root){
  class NavigationHistory {
    constructor(initial, limit=64){this.current=initial;this.past=[];this.future=[];this.limit=limit;}
    visit(next){if(JSON.stringify(next)===JSON.stringify(this.current))return;this.past.push(this.current);if(this.past.length>this.limit)this.past.shift();this.current=next;this.future=[];}
    back(){if(!this.past.length)return null;this.future.push(this.current);this.current=this.past.pop();return this.current;}
    forward(){if(!this.future.length)return null;this.past.push(this.current);this.current=this.future.pop();return this.current;}
    reset(next){this.current=next;this.past=[];this.future=[];}
  }
  if(typeof module!=='undefined')module.exports=NavigationHistory;
  else root.GravitasHistory=NavigationHistory;
})(globalThis);
