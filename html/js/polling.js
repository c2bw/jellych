export function createPollingLoop(task, options = {}){
  const delayMs = options.delayMs ?? 5000;
  const setTimer = options.setTimer || ((callback, delay)=>setTimeout(callback, delay));
  const onError = options.onError || ((error)=>console.error(error));
  let started = false;

  const schedule = ()=>setTimer(run, delayMs);
  async function run(){
    try{
      await task();
    }catch(error){
      onError(error);
    }finally{
      schedule();
    }
  }

  return function start(){
    if(started) return;
    started = true;
    schedule();
  };
}
