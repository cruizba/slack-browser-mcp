import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import { parsePrintedReference } from '../src/slack/labels.ts'

describe('parsePrintedReference', () => {
  it('splits the id out of the label a listing prints', () => {
    assert.deepEqual(parsePrintedReference('dm:Ana Ruiz (D01ANA)'), {
      id: 'D01ANA',
      reference: 'dm:Ana Ruiz',
    })
    assert.deepEqual(parsePrintedReference('#random (C01RANDOM)'), {
      id: 'C01RANDOM',
      reference: '#random',
    })
    assert.deepEqual(parsePrintedReference('private:leads (C02LEADS)'), {
      id: 'C02LEADS',
      reference: 'private:leads',
    })
  })

  it('drops the member count the same line carries', () => {
    assert.deepEqual(parsePrintedReference('#general (C01GENERAL) 12 members'), {
      id: 'C01GENERAL',
      reference: '#general',
    })
    assert.deepEqual(parsePrintedReference('dm:Ana Ruiz (D01ANA) 1 member'), {
      id: 'D01ANA',
      reference: 'dm:Ana Ruiz',
    })
  })

  it('leaves a reference without a printed id alone', () => {
    assert.deepEqual(parsePrintedReference('dm:Ana Ruiz'), { reference: 'dm:Ana Ruiz' })
    assert.deepEqual(parsePrintedReference('C01RANDOM'), { reference: 'C01RANDOM' })
    assert.deepEqual(parsePrintedReference('  #random  '), { reference: '#random' })
    assert.deepEqual(parsePrintedReference('grupo (de trabajo)'), {
      reference: 'grupo (de trabajo)',
    })
  })
})
