import React from 'react'
import { Button, useNotify, useTranslate } from 'react-admin'
import { useDispatch } from 'react-redux'
import { MdAutoAwesome } from 'react-icons/md'
import { playPersonalMix } from './playbackActions'
import config from '../config'

export const PersonalMixButton = ({ size = 100 }) => {
  const translate = useTranslate()
  const dispatch = useDispatch()
  const notify = useNotify()

  if (!config.personalMixEnabled) {
    return null
  }

  const handleOnClick = async () => {
    notify('message.buildingPersonalMix', 'info')
    try {
      await playPersonalMix(dispatch, notify, { size })
    } catch (e) {
      notify('ra.page.error', 'warning')
    }
  }

  return (
    <Button
      onClick={handleOnClick}
      label={translate('resources.song.actions.personalMix')}
    >
      <MdAutoAwesome />
    </Button>
  )
}

export default PersonalMixButton
