// Parse supplied text with the pinned compiler. Never load/evaluate project files.
const fs=require('fs'),vm=require('vm');
const input=JSON.parse(fs.readFileSync(0,'utf8'));
const compilerModule={exports:{}};
vm.runInNewContext(input.compiler,{module:compilerModule,exports:compilerModule.exports},{timeout:10000});
const ts=compilerModule.exports;
function analyze(source){
 const out={path:source.path,symbols:[],imports:[],calls:[],blocked:{},exports:{}};
 const file=ts.createSourceFile(source.path,source.content,ts.ScriptTarget.Latest,true,ts.getScriptKindFromFileName(source.path));
 if(file.parseDiagnostics.length){out.parse_error='SyntaxError at line '+(file.getLineAndCharacterOfPosition(file.parseDiagnostics[0].start||0).line+1);return out}
 let scope='';const declared=new Map(),cjsExports=new Set();
 const exportName=(key,value,cjs=false)=>{if(!key)return;out.exports[key]=Object.hasOwn(out.exports,key)?'':value;if(cjs)cjsExports.add(key)};
 const owner=()=>source.path+'::'+scope;
 const line=n=>file.getLineAndCharacterOfPosition(n.getStart(file)).line+1;
 const name=n=>n&&(ts.isIdentifier(n)||ts.isStringLiteral(n)||ts.isNumericLiteral(n))?n.text:'';
 const dotted=n=>ts.isIdentifier(n)?n.text:ts.isPropertyAccessExpression(n)?(dotted(n.expression)?dotted(n.expression)+'.'+n.name.text:''):'';
 const block=n=>{if(!n)return;const key=owner();if(!out.blocked[key])out.blocked[key]=new Set();out.blocked[key].add(n)};
 function blockPattern(n){if(ts.isIdentifier(n))block(n.text);else if(ts.isObjectBindingPattern(n)||ts.isArrayBindingPattern(n))for(const e of n.elements)if(ts.isBindingElement(e))blockPattern(e.name)}
 const flags=(n,kind)=>!!n.modifiers?.some(m=>m.kind===kind);
 function definition(n,label,kind,body){
  const parent=owner(),qualified=(scope?scope+'.':'')+label;
  const key=parent+'\0'+label;declared.set(key,(declared.get(key)||0)+1);
  out.symbols.push({id:source.path+'::'+qualified,name:qualified,kind,static:flags(n,ts.SyntaxKind.StaticKeyword),line:line(n),end_line:file.getLineAndCharacterOfPosition(n.end).line+1,parent});
  if(!scope&&flags(n,ts.SyntaxKind.ExportKeyword))exportName(flags(n,ts.SyntaxKind.DefaultKeyword)?'default':label,qualified);
  const previous=scope;scope=qualified;
  for(const p of n.parameters||[])blockPattern(p.name);
  body();scope=previous;
 }
 function addImport(module,alias,member,n,typeOnly=false,commonJS=false){
  if(typeOnly)block(alias)
  out.imports.push({owner:owner(),name:module,member:member||'',alias,from:!!member,explicit_alias:true,type_only:typeOnly,common_js:commonJS,line:line(n),level:0});
 }
 function requires(n){return n&&ts.isCallExpression(n)&&ts.isIdentifier(n.expression)&&n.expression.text==='require'&&n.arguments.length===1&&ts.isStringLiteral(n.arguments[0])?n.arguments[0].text:null}
 function exportAssignment(n){
  if(scope||!ts.isBinaryExpression(n)||n.operatorToken.kind!==ts.SyntaxKind.EqualsToken)return false;
  const target=dotted(n.left),value=n.right;
  if(target==='module.exports'){
   if(ts.isIdentifier(value))exportName('default',value.text,true);
   else if(ts.isObjectLiteralExpression(value))for(const p of value.properties){if(ts.isShorthandPropertyAssignment(p))exportName(p.name.text,p.name.text,true);else if(ts.isPropertyAssignment(p)&&ts.isIdentifier(p.initializer))exportName(name(p.name),p.initializer.text,true)}
   return true;
  }
  if(target.startsWith('exports.')||target.startsWith('module.exports.')){if(ts.isIdentifier(value))exportName(target.split('.').at(-1),value.text,true);return true}
  return false;
 }
 function visit(n){
  if(ts.isImportDeclaration(n)&&ts.isStringLiteral(n.moduleSpecifier)){
   const mod=n.moduleSpecifier.text,c=n.importClause;
   if(!c)addImport(mod,'','',n);
   else {if(c.name)addImport(mod,c.name.text,'default',n,c.isTypeOnly);const b=c.namedBindings;if(b&&ts.isNamespaceImport(b))addImport(mod,b.name.text,'',n,c.isTypeOnly);else if(b)for(const e of b.elements)addImport(mod,e.name.text,(e.propertyName||e.name).text,n,c.isTypeOnly||e.isTypeOnly)}
   return;
  }
  if(ts.isImportEqualsDeclaration(n)&&ts.isExternalModuleReference(n.moduleReference)&&ts.isStringLiteral(n.moduleReference.expression)){addImport(n.moduleReference.expression.text,n.name.text,'',n,n.isTypeOnly,true);return}
  if(ts.isExportDeclaration(n)){
   // Re-exports and stars remain explicit unresolved imports, never guessed aliases.
   if(n.moduleSpecifier&&ts.isStringLiteral(n.moduleSpecifier))addImport(n.moduleSpecifier.text,'','',n);
   else if(n.exportClause&&ts.isNamedExports(n.exportClause))for(const e of n.exportClause.elements)if(!e.isTypeOnly)exportName(e.name.text,(e.propertyName||e.name).text);
   return;
  }
  if(ts.isExportAssignment(n)){if(ts.isIdentifier(n.expression))exportName('default',n.expression.text);return}
  if(ts.isFunctionDeclaration(n)||ts.isClassDeclaration(n)||ts.isInterfaceDeclaration(n)||ts.isTypeAliasDeclaration(n)||ts.isEnumDeclaration(n)){
   const label=name(n.name)||(flags(n,ts.SyntaxKind.DefaultKeyword)?'default':'');if(!label)return;
   const kind=ts.isFunctionDeclaration(n)?(flags(n,ts.SyntaxKind.AsyncKeyword)?'async_function':'function'):ts.isClassDeclaration(n)?'class':ts.isInterfaceDeclaration(n)?'interface':ts.isEnumDeclaration(n)?'enum':'type';
   definition(n,label,kind,()=>ts.forEachChild(n,visit));return;
  }
  if(ts.isVariableDeclaration(n)){
   const mod=requires(n.initializer);
   if(mod!==null){if(ts.isIdentifier(n.name))addImport(mod,n.name.text,'',n,false,true);else if(ts.isObjectBindingPattern(n.name))for(const e of n.name.elements)if(!e.dotDotDotToken&&!e.initializer&&ts.isIdentifier(e.name))addImport(mod,e.name.text,name(e.propertyName)||e.name.text,n,false,true);return}
   const value=n.initializer;
   if(ts.isIdentifier(n.name)&&value&&(ts.isArrowFunction(value)||ts.isFunctionExpression(value)||ts.isObjectLiteralExpression(value))){
    const exported=flags(n.parent.parent,ts.SyntaxKind.ExportKeyword);const label=n.name.text;
    if(exported&&!scope)exportName(label,label);
    definition(value,label,ts.isObjectLiteralExpression(value)?'object':flags(value,ts.SyntaxKind.AsyncKeyword)?'async_function':'function',()=>ts.forEachChild(value,visit));return;
   }
   blockPattern(n.name);
  }
  if(ts.isMethodDeclaration(n)||ts.isGetAccessorDeclaration(n)||ts.isSetAccessorDeclaration(n)||ts.isMethodSignature(n)){
   const label=name(n.name);if(label)definition(n,label,'method',()=>ts.forEachChild(n,visit));return;
  }
  if(ts.isPropertyAssignment(n)&&name(n.name)&&(ts.isArrowFunction(n.initializer)||ts.isFunctionExpression(n.initializer))){definition(n.initializer,name(n.name),'method',()=>ts.forEachChild(n.initializer,visit));return}
  if(ts.isArrowFunction(n)||ts.isFunctionExpression(n))return; // Unnamed scopes have no durable identity.
  if(ts.isCallExpression(n)||ts.isNewExpression(n))out.calls.push({owner:owner(),name:dotted(n.expression)||'<dynamic>',line:line(n)});
  if(ts.isBinaryExpression(n)&&n.operatorToken.kind>=ts.SyntaxKind.FirstAssignment&&n.operatorToken.kind<=ts.SyntaxKind.LastAssignment){if(!exportAssignment(n))block(dotted(n.left).split('.')[0])}
  if(ts.isParameter(n))blockPattern(n.name);
  ts.forEachChild(n,visit);
 }
 visit(file);
 for(const[key,count]of declared)if(count>1){const[owner,label]=key.split('\0');if(!out.blocked[owner])out.blocked[owner]=new Set();out.blocked[owner].add(label)}
 function shadowed(key,label){for(;;){if(out.blocked[key]?.has(label)||declared.has(key+'\0'+label))return true;const q=key.slice((source.path+'::').length);if(!q)return false;key=source.path+'::'+(q.includes('.')?q.slice(0,q.lastIndexOf('.')):'')}}
 for(const item of out.imports)if(item.common_js&&shadowed(item.owner,'require'))item.uncertain=true;
 if(shadowed(source.path+'::','module')||shadowed(source.path+'::','exports'))for(const key of cjsExports)delete out.exports[key];
 out.blocked=Object.fromEntries(Object.entries(out.blocked).map(([key,values])=>[key,[...values].sort()]));
 return out;
}
const files=input.files.map(source=>{try{return analyze(source)}catch(e){return {path:source.path,symbols:[],imports:[],calls:[],blocked:{},exports:{},parse_error:(e instanceof RangeError?'RecursionError':'ASTError')+' at line 0'}}});
process.stdout.write(JSON.stringify({node:process.versions.node,typescript:ts.version,files}));
