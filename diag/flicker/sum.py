import json,sys
for p in sys.argv[1:]:
    try:
        s=json.load(open(p))['summary']
    except Exception as e:
        print(p, 'ERR', e); continue
    i=s['info']
    big=s['pixBig']
    print(f"{s['name']:<22} zoom={i.get('zoom','')} dpr={i['dpr']} sb={i['scrollbar']} lines={i['lines']} keys={s['keys']} dom={s['domStates']} pix={s['pixFrames']} big={len(big)} flashes={len(s.get('flashes',[]))} trans={len(s['transients'])} inkDips={len(s['inkDips'])} sseDips={len(s['sseDips'])} maxShift={max([x['v'] for x in s['layoutShifts']] or [0]):.2e} box-cursor={s.get('boxFromCursor')} scrolled={s.get('scrolled')} flips={ {k:v for k,v in s['flipCounts'].items() if k not in ('th','ink','cx','inp','off','out')} } net={ {k:v for k,v in s['net'].items() if 'size' in k} }")
    for b in big[:3]: print('     BIG', b)
